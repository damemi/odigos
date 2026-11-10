package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/odigos-io/odigos/central-backend/graph/model"
	redisclient "github.com/odigos-io/odigos/central-backend/internal/redis"
	"github.com/odigos-io/odigos/central-backend/internal/ssehub"
)

type HealthService struct {
	redisClient  *redisclient.RedisClient
	adminService *AdminService
	sseHub       *ssehub.Hub
}

func NewHealthService(
	redisClient *redisclient.RedisClient,
	adminService *AdminService,
	sseHub *ssehub.Hub,
) *HealthService {
	return &HealthService{
		redisClient:  redisClient,
		adminService: adminService,
		sseHub:       sseHub,
	}
}

var (
	REDIS_JOB_ID    = "redis"
	KEYCLOAK_JOB_ID = "keycloak"
	SSE_JOB_ID      = "sse"
)

type HealthJobResult struct {
	Status model.ServicesStatus
	JobID  string
}

func (svc *HealthService) buildHealthResponse(results map[string]model.ServicesStatus) *model.BackendHealthStatus {
	redis := results[REDIS_JOB_ID]
	keycloak := results[KEYCLOAK_JOB_ID]
	sse := results[SSE_JOB_ID]
	return &model.BackendHealthStatus{
		RedisStatus:    &redis,
		KeycloakStatus: &keycloak,
		SseStatus:      &sse,
	}
}

func (svc *HealthService) GetBackendHealthStatus(ctx context.Context) (*model.BackendHealthStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()

	var jobs = map[string]func(string) HealthJobResult{
		REDIS_JOB_ID: func(jobID string) HealthJobResult {
			return HealthJobResult{
				Status: svc.redisClient.HealthCheck(ctx),
				JobID:  jobID,
			}
		},
		KEYCLOAK_JOB_ID: func(jobID string) HealthJobResult {
			return HealthJobResult{
				Status: svc.adminService.kcAdmin.HealthCheck(ctx),
				JobID:  jobID,
			}
		},
		SSE_JOB_ID: func(jobId string) HealthJobResult {
			return HealthJobResult{
				Status: svc.sseHub.HealthCheck(ctx),
				JobID:  jobId,
			}
		},
	}

	var wg sync.WaitGroup
	results := make(map[string]model.ServicesStatus)

	ch := make(chan HealthJobResult, len(jobs))

	// Pre-populate defaults so buildHealthResponse always has a value for each job (no extra wrapper needed).
	for jobID := range jobs {
		downMsg := fmt.Sprintf("%s service did not respond", jobID)
		results[jobID] = model.ServicesStatus{
			Status:  model.HealthStatusDown,
			Message: &downMsg,
		}
	}

	for jobID, jobFunc := range jobs {
		wg.Add(1)
		go func(id string, fn func(string) HealthJobResult) {
			defer wg.Done()
			ch <- fn(id)
		}(jobID, jobFunc)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	for {
		select {
		case <-ctx.Done():
			return svc.buildHealthResponse(results), nil
		case result, ok := <-ch:
			if !ok {
				return svc.buildHealthResponse(results), nil
			}
			results[result.JobID] = result.Status
		}
	}
}

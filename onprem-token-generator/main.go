package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/profiles"
)

const (
	defaultExpiryDays = 365 // Default to 1 year
	issuer            = "https://odigos.io"
	subject           = "https://odigos.io/onprem"
)

func verifyProfiles(clientProfiles *[]string) {
	var validProfiles []string

	for _, profile := range *clientProfiles {
		if _, exists := profiles.ProfilesByName[common.ProfileName(profile)]; exists {
			validProfiles = append(validProfiles, profile)
		} else {
			fmt.Printf("\033[31mERROR\033[0m %s is invalid client profile, aborting\n", profile)
			os.Exit(1)
		}
	}

	*clientProfiles = validProfiles
}

func getExpTime() (int64, error) {
	durationStr, exists := os.LookupEnv("TOKEN_EXPIRY_DURATION")
	if exists {
		duration, err := time.ParseDuration(durationStr)
		if err != nil {
			return 0, fmt.Errorf("failed to parse expiry duration: %w", err)
		}
		expTime := time.Now().Add(duration).Unix()
		return expTime, nil
	}

	// Get the number of days from environment variable or default to 365
	daysStr, exists := os.LookupEnv("TOKEN_EXPIRY_DAYS")
	days := defaultExpiryDays
	if exists {
		parsedDays, err := strconv.Atoi(daysStr)
		if err != nil {
			return 0, err
		}
		if parsedDays <= 0 {
			return 0, errors.New("TOKEN_EXPIRY_DAYS must be greater than 0")
		}
		days = parsedDays
	}

	// Calculate expiration time for jwt
	expTime := time.Now().Add(time.Hour * 24 * time.Duration(days)).Unix()

	return expTime, nil
}

func main() {
	// Get the private key from environment variable
	privateKeyPEM, exists := os.LookupEnv("PRIVATE_KEY")
	if !exists {
		log.Fatal("Environment variable PRIVATE_KEY is not set.")
	}

	// Parse the private key
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		log.Fatal("Could not parse private key:", err)
	}

	// Create the token
	token := jwt.New(jwt.SigningMethodRS256)

	// Set claims
	claims := token.Claims.(jwt.MapClaims)

	// issuer
	claims["iss"] = issuer

	// subject
	claims["sub"] = subject

	// expiration time
	expTime, err := getExpTime()
	if err != nil {
		log.Fatal("Could not get expiry time:", err)
	}
	claims["exp"] = expTime

	// issued at
	claims["iat"] = time.Now().Unix()

	// client profile
	profiles, exists := os.LookupEnv("ODIGOS_PROFILES")
	if exists {
		profiles := strings.Split(profiles, ",")
		verifyProfiles(&profiles)
		claims["profiles"] = profiles
	}

	// audience
	audience, exists := os.LookupEnv("JWT_AUDIENCE")
	if !exists || audience == "" {
		log.Fatal("Environment variable JWT_AUDIENCE is not set.")
	}
	claims["aud"] = audience

	// Sign and get the complete encoded token as a string
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		log.Fatal("Could not sign token:", err)
	}

	log.Println("Token:", tokenString)
}

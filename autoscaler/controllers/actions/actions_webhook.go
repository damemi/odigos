/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package actions

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/odigos-io/odigos/api/odigos/v1alpha1"
)

type ActionsValidator struct {
	client.Client
}

var _ webhook.CustomValidator = &ActionsValidator{}

func (s *ActionsValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	action, ok := obj.(*v1alpha1.Action)
	if !ok {
		return nil, fmt.Errorf("expected an Action but got a %T", obj)
	}

	err := s.validateAction(ctx, action)
	if err != nil {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "odigos.io", Kind: "Action"},
			action.Name, err,
		)
	}

	return nil, nil
}

func (s *ActionsValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	old, ok := oldObj.(*v1alpha1.Action)
	if !ok {
		return nil, fmt.Errorf("expected old Action but got a %T", old)
	}
	new, ok := newObj.(*v1alpha1.Action)
	if !ok {
		return nil, fmt.Errorf("expected new Action but got a %T", new)
	}

	err := s.validateAction(ctx, new)
	if err != nil {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "odigos.io", Kind: "Action"},
			new.Name, err,
		)
	}

	return nil, nil
}

func (s *ActionsValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (a *ActionsValidator) validateAction(ctx context.Context, action *v1alpha1.Action) field.ErrorList {
	var allErrs field.ErrorList
	_, _, err := actionProcessorDetails(action)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec").Child("config"), action.Spec.Config, err.Error()))
	}
	return allErrs
}

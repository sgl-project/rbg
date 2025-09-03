package utils

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ErrorInjectingClient struct {
	client.Client
	injectedError error
}

func NewErrorInjectingClient(err error) *ErrorInjectingClient {
	return &ErrorInjectingClient{
		injectedError: err,
	}
}

func (c *ErrorInjectingClient) Get(
	ctx context.Context, key client.ObjectKey,
	obj client.Object, opts ...client.GetOption,
) error {
	return c.injectedError
}

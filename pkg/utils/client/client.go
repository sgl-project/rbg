/*
Copyright 2021 The Kruise Authors.

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

package client

import (
	"fmt"
	"runtime"
	"strings"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	"sigs.k8s.io/rbgs/version"
)

func NewClientFromManager(mgr manager.Manager, name string) client.Client {
	cfg := rest.CopyConfig(mgr.GetConfig())
	cfg.UserAgent = fmt.Sprintf("instanceset-manager/%s", name)

	delegatingClient, _ := client.New(cfg, client.Options{
		Cache: &client.CacheOptions{
			Reader:       mgr.GetCache(),
			Unstructured: true,
		},
	})
	return delegatingClient
}

func NewClientWithUserAgent(mgr manager.Manager, name string) client.Client {
	cfg := rest.CopyConfig(mgr.GetConfig())
	cfg.UserAgent = buildUserAgent(name)

	delegatingClient, _ := client.New(cfg, client.Options{
		Scheme: mgr.GetScheme(),
		Mapper: mgr.GetRESTMapper(),
		Cache: &client.CacheOptions{
			Reader: mgr.GetCache(),
		},
	})
	return delegatingClient
}

func buildUserAgent(name string) string {
	return fmt.Sprintf("%s/%s (%s/%s) %s/%s",
		name,
		adjustVersion(version.Version),
		runtime.GOOS,
		runtime.GOARCH,
		constants.ControllerName,
		adjustCommit(version.GitCommit))
}

func adjustVersion(v string) string {
	if len(v) == 0 {
		return "unknown"
	}
	return strings.SplitN(v, "-", 2)[0]
}

func adjustCommit(c string) string {
	if len(c) == 0 {
		return "unknown"
	}
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

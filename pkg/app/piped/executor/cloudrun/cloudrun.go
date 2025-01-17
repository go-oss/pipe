// Copyright 2020 The PipeCD Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cloudrun

import (
	"context"

	provider "github.com/pipe-cd/pipe/pkg/app/piped/cloudprovider/cloudrun"
	"github.com/pipe-cd/pipe/pkg/app/piped/deploysource"
	"github.com/pipe-cd/pipe/pkg/app/piped/executor"
	"github.com/pipe-cd/pipe/pkg/config"
	"github.com/pipe-cd/pipe/pkg/model"
)

type registerer interface {
	Register(stage model.Stage, f executor.Factory) error
	RegisterRollback(kind model.ApplicationKind, f executor.Factory) error
}

func Register(r registerer) {
	f := func(in executor.Input) executor.Executor {
		return &deployExecutor{
			Input: in,
		}
	}
	r.Register(model.StageCloudRunSync, f)
	r.Register(model.StageCloudRunPromote, f)

	r.RegisterRollback(model.ApplicationKind_CLOUDRUN, func(in executor.Input) executor.Executor {
		return &rollbackExecutor{
			Input: in,
		}
	})
}

func loadServiceManifest(in *executor.Input, serviceManifestFile string, ds *deploysource.DeploySource) (provider.ServiceManifest, bool) {
	in.LogPersister.Infof("Loading service manifest at the %s commit (%s)", ds.RevisionName, ds.Revision)

	sm, err := provider.LoadServiceManifest(ds.AppDir, serviceManifestFile)
	if err != nil {
		in.LogPersister.Errorf("Failed to load service manifest (%v)", err)
		return provider.ServiceManifest{}, false
	}

	in.LogPersister.Infof("Successfully loaded the service manifest at the %s commit", ds.RevisionName)
	return sm, true
}

func findCloudProvider(in *executor.Input) (name string, cfg *config.CloudProviderCloudRunConfig, found bool) {
	name = in.Application.CloudProvider
	if name == "" {
		in.LogPersister.Error("Missing the CloudProvider name in the application configuration")
		return
	}

	cp, ok := in.PipedConfig.FindCloudProvider(name, model.CloudProviderCloudRun)
	if !ok {
		in.LogPersister.Errorf("The specified cloud provider %q was not found in piped configuration", name)
		return
	}

	cfg = cp.CloudRunConfig
	found = true
	return
}

func decideRevisionName(in *executor.Input, sm provider.ServiceManifest, commit string) (revision string, ok bool) {
	var err error
	revision, err = provider.DecideRevisionName(sm, commit)
	if err != nil {
		in.LogPersister.Errorf("Unable to decide revision name for the commit %s (%v)", commit, err)
		return
	}

	ok = true
	return
}

func configureServiceManifest(in *executor.Input, sm provider.ServiceManifest, revision string, traffics []provider.RevisionTraffic) bool {
	if err := sm.SetRevision(revision); err != nil {
		in.LogPersister.Errorf("Unable to set revision name to service manifest (%v)", err)
		return false
	}

	if err := sm.UpdateTraffic(traffics); err != nil {
		in.LogPersister.Errorf("Unable to configure traffic percentages to service manifest (%v)", err)
		return false
	}

	in.LogPersister.Info("Successfully configured revision and traffic percentages to the service manifest")
	for _, t := range traffics {
		in.LogPersister.Infof("  %s: %d", t.RevisionName, t.Percent)
	}

	return true
}

func apply(ctx context.Context, in *executor.Input, cloudProviderName string, cloudProviderCfg *config.CloudProviderCloudRunConfig, sm provider.ServiceManifest) bool {
	in.LogPersister.Info("Start applying the service manifest")
	client, err := provider.DefaultRegistry().Client(ctx, cloudProviderName, cloudProviderCfg, in.Logger)
	if err != nil {
		in.LogPersister.Errorf("Unable to create ClourRun client for the provider (%v)", err)
		return false
	}

	_, err = client.Update(ctx, sm)
	if err == nil {
		in.LogPersister.Infof("Successfully updated the service %s", sm.Name)
		return true
	}

	if err != provider.ErrServiceNotFound {
		in.LogPersister.Errorf("Failed to update the service %s (%v)", sm.Name, err)
		return false
	}

	in.LogPersister.Infof("Service %s was not found, a new service will be created", sm.Name)

	if _, err := client.Create(ctx, sm); err != nil {
		in.LogPersister.Errorf("Failed to create the service %s (%v)", sm.Name, err)
		return false
	}

	in.LogPersister.Infof("Successfully created the service %s", sm.Name)
	return true
}

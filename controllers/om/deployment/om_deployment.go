package deployment

import (
	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/replicaset"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"go.uber.org/zap"
)

// CreateFromReplicaSet builds the replica set for the automation config
// based on the given MongoDB replica set.
// NOTE: This method is only used for testing.
// But we can't move in a *_test file since it is called from tests in
// different packages. And test files are only compiled
// when testing that specific package
// https://github.com/golang/go/issues/10184#issuecomment-84465873
func CreateFromReplicaSet(rs *mdbv1.MongoDB) om.Deployment {
	sts := construct.DatabaseStatefulSet(*rs, construct.ReplicaSetOptions(
		func(options *construct.DatabaseStatefulSetOptions) {
			options.PodVars = &env.PodEnvVars{ProjectID: "abcd"}

		},
	), nil)
	d := om.NewDeployment()

	lastConfig, err := rs.GetLastAdditionalMongodConfigByType(mdbv1.ReplicaSetConfig)
	if err != nil {
		panic(err)
	}

	d.MergeReplicaSet(
		replicaset.BuildFromStatefulSet(sts, rs.GetSpec()),
		rs.Spec.AdditionalMongodConfig.ToMap(),
		lastConfig.ToMap(),
		nil,
	)
	d.AddMonitoringAndBackup(zap.S(), rs.Spec.GetSecurity().IsTLSEnabled(), util.CAFilePathInContainer)
	d.ConfigureTLS(rs.Spec.GetSecurity(), util.CAFilePathInContainer)
	return d
}

// Link returns the deployment link given the baseUrl and groupId.
func Link(url, groupId string) string {
	return fmt.Sprintf("%s/v2/%s", url, groupId)
}

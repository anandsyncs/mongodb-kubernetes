package deployment

import (
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/replicaset"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	mock.InitDefaultEnvVariables()
}

// TestPrepareScaleDown_OpsManagerRemovedMember tests the situation when during scale down some replica set member doesn't
// exist (this can happen when for example the member was removed from Ops Manager manually). The exception is handled
// and only the existing member is marked as unvoted
func TestPrepareScaleDown_OpsManagerRemovedMember(t *testing.T) {
	// This is deployment with 2 members (emulating that OpsManager removed the 3rd one)
	rs := mdbv1.NewReplicaSetBuilder().SetName("bam").SetMembers(2).Build()
	oldDeployment := CreateFromReplicaSet(rs)
	mockedOmConnection := om.NewMockedOmConnection(oldDeployment)

	// We try to prepare two members for scale down, but one of them will fail (bam-2)
	rsWithThreeMembers := map[string][]string{"bam": {"bam-1", "bam-2"}}
	assert.NoError(t, replicaset.PrepareScaleDownFromMap(mockedOmConnection, rsWithThreeMembers, zap.S()))

	expectedDeployment := CreateFromReplicaSet(rs)

	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted("bam", []string{"bam-1"}))

	mockedOmConnection.CheckNumberOfUpdateRequests(t, 1)
	mockedOmConnection.CheckDeployment(t, expectedDeployment)
}

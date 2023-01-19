package mdb

import (
	"testing"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMongoDB_ProcessValidations_BadHorizonsMemberCount(t *testing.T) {
	replicaSetHorizons := []MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12346"},
	}

	rs := NewReplicaSetBuilder().SetSecurityTLSEnabled().Build()
	rs.Spec.Connectivity = &MongoDBConnectivity{}
	rs.Spec.Connectivity.ReplicaSetHorizons = replicaSetHorizons
	err := rs.ProcessValidationsOnReconcile(nil)
	assert.Contains(t, "Number of horizons must be equal to number of members in replica set", err.Error())
}

func TestMongoDB_ProcessValidations_HorizonsWithoutTLS(t *testing.T) {
	replicaSetHorizons := []MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12342"},
		{"my-horizon": "my-db.com:12346"},
	}

	rs := NewReplicaSetBuilder().Build()
	rs.Spec.Connectivity = &MongoDBConnectivity{}
	rs.Spec.Connectivity.ReplicaSetHorizons = replicaSetHorizons
	err := rs.ProcessValidationsOnReconcile(nil)
	assert.Equal(t, "TLS must be enabled in order to use replica set horizons", err.Error())
}

func TestMongoDB_ProcessValidationsOnReconcile_X509WithoutTls(t *testing.T) {
	rs := NewReplicaSetBuilder().Build()
	rs.Spec.Security.Authentication = &Authentication{Enabled: true, Modes: []string{"X509"}}
	err := rs.ProcessValidationsOnReconcile(nil)
	assert.Equal(t, "Cannot have a non-tls deployment when x509 authentication is enabled", err.Error())
}

func TestMongoDB_ValidateCreate_Error(t *testing.T) {
	replicaSetHorizons := []MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12342"},
		{"my-horizon": "my-db.com:12346"},
	}

	rs := NewReplicaSetBuilder().Build()
	rs.Spec.Connectivity = &MongoDBConnectivity{}
	rs.Spec.Connectivity.ReplicaSetHorizons = replicaSetHorizons
	err := rs.ValidateCreate()
	assert.Equal(t, "TLS must be enabled in order to use replica set horizons", err.Error())
}

func TestMongoDB_MultipleAuthsButNoAgentAuth_Error(t *testing.T) {
	rs := NewReplicaSetBuilder().SetVersion("4.0.2-ent").Build()
	rs.Spec.Security = &Security{
		TLSConfig: &TLSConfig{Enabled: true},
		Authentication: &Authentication{
			Enabled: true,
			Modes:   []string{"LDAP", "X509"},
		},
	}
	err := rs.ValidateCreate()
	assert.Errorf(t, err, "spec.security.authentication.agents.mode must be specified if more than one entry is present in spec.security.authentication.modes")
}

func TestMongoDB_ResourceTypeImmutable(t *testing.T) {
	newRs := NewReplicaSetBuilder().Build()
	oldRs := NewReplicaSetBuilder().setType(ShardedCluster).Build()
	err := newRs.ValidateUpdate(oldRs)
	assert.Errorf(t, err, "'resourceType' cannot be changed once created")
}

func TestSpecProjectOnlyOneValue(t *testing.T) {
	rs := NewReplicaSetBuilder().Build()
	rs.Spec.CloudManagerConfig = &PrivateCloudConfig{
		ConfigMapRef: ConfigMapRef{Name: "cloud-manager"},
	}
	err := rs.ValidateCreate()
	assert.NoError(t, err)
}

func TestMongoDB_ProcessValidations(t *testing.T) {
	rs := NewReplicaSetBuilder().Build()
	assert.Error(t, rs.ProcessValidationsOnReconcile(nil), nil)
}

func TestMongoDB_ValidateAdditionalMongodConfig(t *testing.T) {
	t.Run("No sharded cluster additional config for replica set", func(t *testing.T) {
		rs := NewReplicaSetBuilder().SetConfigSrvAdditionalConfig(NewAdditionalMongodConfig("systemLog.verbosity", 5)).Build()
		err := rs.ValidateCreate()
		require.Error(t, err)
		assert.Equal(t, "'spec.mongos', 'spec.configSrv', 'spec.shard' cannot be specified if type of MongoDB is ReplicaSet", err.Error())
	})
	t.Run("No sharded cluster additional config for standalone", func(t *testing.T) {
		rs := NewStandaloneBuilder().SetMongosAdditionalConfig(NewAdditionalMongodConfig("systemLog.verbosity", 5)).Build()
		err := rs.ValidateCreate()
		require.Error(t, err)
		assert.Equal(t, "'spec.mongos', 'spec.configSrv', 'spec.shard' cannot be specified if type of MongoDB is Standalone", err.Error())
	})
	t.Run("No replica set additional config for sharded cluster", func(t *testing.T) {
		rs := NewClusterBuilder().SetAdditionalConfig(NewAdditionalMongodConfig("systemLog.verbosity", 5)).Build()
		err := rs.ValidateCreate()
		require.Error(t, err)
		assert.Equal(t, "'spec.additionalMongodConfig' cannot be specified if type of MongoDB is ShardedCluster", err.Error())
	})
}

func TestScramSha1AuthValidation(t *testing.T) {
	type TestConfig struct {
		MongoDB       *MongoDB
		ErrorExpected bool
	}
	tests := map[string]TestConfig{
		"Valid MongoDB with Authentication": {
			MongoDB:       NewReplicaSetBuilder().EnableAuth([]string{util.SCRAMSHA1}).Build(),
			ErrorExpected: true,
		},
		"Valid MongoDB with SCRAM-SHA-1": {
			MongoDB:       NewReplicaSetBuilder().EnableAuth([]string{util.SCRAMSHA1, util.MONGODBCR}).EnableAgentAuth(util.MONGODBCR).Build(),
			ErrorExpected: false,
		},
	}
	for testName, testConfig := range tests {
		t.Run(testName, func(t *testing.T) {
			validationResult := scramSha1AuthValidation(testConfig.MongoDB.Spec.DbCommonSpec)
			assert.Equal(t, testConfig.ErrorExpected, v1.ValidationSuccess() != validationResult, "Expected %v, got %v", testConfig.ErrorExpected, validationResult)
		})

	}
}

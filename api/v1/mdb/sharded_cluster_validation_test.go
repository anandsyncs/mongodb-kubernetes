package mdb

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"

	v1 "github.com/mongodb/mongodb-kubernetes-operator/api/v1"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
)

func makeMemberConfig(members int) []automationconfig.MemberOptions {
	return make([]automationconfig.MemberOptions, members)
}

var defaultMemberConfig = makeMemberConfig(1)

func TestShardOverridesAreCorrect(t *testing.T) {
	intPointer := ptr.To(3)
	resourceName := "foo"
	tests := []struct {
		name                   string
		isMultiCluster         bool
		shardCount             int
		shardOverrides         []ShardOverride
		expectError            bool
		errorMessage           string
		expectWarning          bool
		expectedWarningMessage string
	}{
		{
			name:           "Validate correct shard overrides for SingleCluster topology",
			isMultiCluster: false,
			shardCount:     3,
			shardOverrides: []ShardOverride{{ShardNames: []string{"foo-1"}}, {ShardNames: []string{"foo-0", "foo-2"}}},
		},
		{
			name:           "Validate incorrect shard overrides for SingleCluster topology",
			isMultiCluster: false,
			shardCount:     3,
			shardOverrides: []ShardOverride{{ShardNames: []string{"foo-100"}}, {ShardNames: []string{"foo-3"}}},
			expectError:    true,
			errorMessage:   "name foo-100 is incorrect, it must follow the following format: foo-{shard index} with shardIndex < 3 (shardCount)",
		},
		{
			name:           "No error for correct shard overrides",
			isMultiCluster: true,
			shardCount:     4,
			shardOverrides: []ShardOverride{{ShardNames: []string{"foo-2"}}, {ShardNames: []string{"foo-0", "foo-3"}}},
		},
		{
			name:           "Error for incorrect shard name",
			isMultiCluster: true,
			shardCount:     3,
			shardOverrides: []ShardOverride{{ShardNames: []string{"foo-4"}}, {ShardNames: []string{"foo-0", "foo-1"}}},
			expectError:    true,
			errorMessage:   "name foo-4 is incorrect, it must follow the following format: foo-{shard index} with shardIndex < 3 (shardCount)",
		},
		{
			name:           "Error for incorrect shard name with leading zeros",
			isMultiCluster: true,
			shardCount:     3,
			shardOverrides: []ShardOverride{{ShardNames: []string{"foo-000"}}, {ShardNames: []string{"foo-0", "foo-1"}}},
			expectError:    true,
			errorMessage:   "name foo-000 is incorrect, it must follow the following format: foo-{shard index} with shardIndex < 3 (shardCount)",
		},
		{
			name:           "Error for duplicate shard names",
			isMultiCluster: true,
			shardCount:     3,
			shardOverrides: []ShardOverride{{ShardNames: []string{"foo-2"}}, {ShardNames: []string{"foo-0", "foo-2"}}},
			expectError:    true,
			errorMessage:   "spec.shardOverride[*].shardNames elements must be unique in shardOverrides, shardName foo-2 is a duplicate",
		},
		{
			name:           "Error for empty shard names slice",
			isMultiCluster: true,
			shardCount:     3,
			shardOverrides: []ShardOverride{{ShardNames: []string{}}},
			expectError:    true,
			errorMessage:   "spec.shardOverride[*].shardNames cannot be empty, shardOverride with index 0 is invalid",
		},
		{
			name:           "Error when ClusterName is empty in ClusterSpecList",
			isMultiCluster: true,
			shardCount:     2,
			shardOverrides: []ShardOverride{
				{
					ShardNames: []string{"foo-0"},
					ShardedClusterComponentOverrideSpec: ShardedClusterComponentOverrideSpec{
						ClusterSpecList: []ClusterSpecItemOverride{{ClusterName: "", Members: intPointer}},
					},
				},
			},
			expectError:  true,
			errorMessage: "shard override for shards [foo-0] has an empty clusterName in clusterSpecList, this field must be specified",
		},
		{
			name:           "Error when ClusterSpecList is empty in ShardOverrides",
			isMultiCluster: true,
			shardCount:     5,
			shardOverrides: []ShardOverride{
				{
					ShardNames: []string{"foo-0", "foo-1", "foo-4"},
					ShardedClusterComponentOverrideSpec: ShardedClusterComponentOverrideSpec{
						ClusterSpecList: []ClusterSpecItemOverride{},
					},
				},
			},
			expectError:  true,
			errorMessage: "shard override for shards [foo-0 foo-1 foo-4] has an empty clusterSpecList",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sc *MongoDB
			if tt.isMultiCluster {
				sc = NewDefaultMultiShardedClusterBuilder().SetName(resourceName).Build()
			} else {
				sc = NewDefaultShardedClusterBuilder().SetName(resourceName).Build()
			}
			sc.Spec.ShardCount = tt.shardCount
			sc.Spec.ShardOverrides = tt.shardOverrides

			_, err := sc.ValidateCreate()

			if tt.expectError {
				require.Error(t, err)
				assert.Equal(t, tt.errorMessage, err.Error())
			} else {
				assert.NoError(t, err)
			}

			if tt.expectWarning {
				assertWarningExists(t, sc.Status.Warnings, status.Warning(tt.expectedWarningMessage))
			}
		})
	}
}

func TestValidClusterSpecLists(t *testing.T) {
	tests := []struct {
		name          string
		shardSpec     ClusterSpecItem
		configSrvSpec ClusterSpecItem
		mongosSpec    ClusterSpecItem
		members       int
		memberConfig  int
		expectError   bool
		errorMessage  string
	}{
		{
			name:          "Error when Members and MemberConfig mismatch",
			shardSpec:     ClusterSpecItem{ClusterName: "shard-cluster", Members: 3},
			configSrvSpec: ClusterSpecItem{ClusterName: "config-cluster", Members: 1},
			mongosSpec:    ClusterSpecItem{ClusterName: "mongos-cluster", Members: 1},
			members:       3,
			memberConfig:  2,
			expectError:   true,
			errorMessage:  "Invalid clusterSpecList: " + MemberConfigErrorMessage,
		},
		{
			name:          "No error when ClusterSpecLists are valid",
			shardSpec:     ClusterSpecItem{ClusterName: "shard-cluster", Members: 3},
			configSrvSpec: ClusterSpecItem{ClusterName: "config-cluster", Members: 1},
			mongosSpec:    ClusterSpecItem{ClusterName: "mongos-cluster", Members: 1},
			members:       3,
			memberConfig:  3,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := NewDefaultMultiShardedClusterBuilder().Build()
			sc.Spec.ShardSpec.ClusterSpecList = ClusterSpecList{tt.shardSpec}
			sc.Spec.ConfigSrvSpec.ClusterSpecList = ClusterSpecList{tt.configSrvSpec}
			sc.Spec.MongosSpec.ClusterSpecList = ClusterSpecList{tt.mongosSpec}
			sc.Spec.Members = tt.members
			sc.Spec.MemberConfig = make([]automationconfig.MemberOptions, tt.memberConfig)

			_, err := sc.ValidateCreate()

			if tt.expectError {
				require.Error(t, err)
				assert.Equal(t, tt.errorMessage, err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNoIgnoredFieldUsed(t *testing.T) {
	tests := []struct {
		name              string
		isMultiCluster    bool
		mongodsPerShard   int
		configServerCount int
		mongosCount       int
		shardOverrides    []ShardOverride
		expectWarning     bool
		expectError       bool
		errorMessage      string
		expectedWarnings  []status.Warning
	}{
		{
			name:              "No warning when fields are set in SingleCluster topology",
			isMultiCluster:    false,
			mongodsPerShard:   3,
			configServerCount: 2,
			mongosCount:       2,
			shardOverrides: []ShardOverride{
				{ShardNames: []string{"foo-0"}, MemberConfig: defaultMemberConfig},
				{ShardNames: []string{"foo-1"}, Members: ptr.To(2)},
				{ShardNames: []string{"foo-2"}, StatefulSetConfiguration: &v1.StatefulSetConfiguration{}},
			},
			expectWarning:    false,
			expectedWarnings: []status.Warning{},
		},
		{
			name:           "No warning when no ignored fields are used in MultiCluster topology",
			isMultiCluster: true,
		},
		{
			name:            "Error when MongodsPerShardCount is set in MultiCluster topology",
			isMultiCluster:  true,
			mongodsPerShard: 3,
			expectError:     true,
			errorMessage:    "spec.mongodsPerShardCount must not be set in Multi Cluster topology. The member count will depend on: spec.shard.clusterSpecList.members",
		},
		{
			name:              "Error when ConfigServerCount is set in MultiCluster topology",
			isMultiCluster:    true,
			configServerCount: 2,
			expectError:       true,
			errorMessage:      "spec.configServerCount must not be set in Multi Cluster topology. The member count will depend on: spec.configSrv.clusterSpecList.members",
		},
		{
			name:           "Error when MongosCount is set in MultiCluster topology",
			isMultiCluster: true,
			mongosCount:    2,
			expectError:    true,
			errorMessage:   "spec.mongosCount must not be set in Multi Cluster topology. The member count will depend on: spec.mongos.clusterSpecList.members",
		},
		{
			name:           "Warning when MemberConfig is set in ShardOverrides in MultiCluster topology",
			isMultiCluster: true,
			shardOverrides: []ShardOverride{
				{ShardNames: []string{"foo-0"}, MemberConfig: defaultMemberConfig},
			},
			expectWarning: true,
			expectedWarnings: []status.Warning{
				"spec.shardOverrides.memberConfig is ignored in Multi Cluster topology. Use instead: spec.shardOverrides.clusterSpecList.memberConfig",
			},
		},
		{
			name:           "Warning when Members is set in ShardOverrides in MultiCluster topology",
			isMultiCluster: true,
			shardOverrides: []ShardOverride{
				{ShardNames: []string{"foo-0"}, Members: ptr.To(2)},
			},
			expectWarning: true,
			expectedWarnings: []status.Warning{
				"spec.shardOverrides.members is ignored in Multi Cluster topology. Use instead: spec.shardOverrides.clusterSpecList.members",
			},
		},
		{
			name:           "Multiple warnings when several ignored fields are set in MultiCluster topology",
			isMultiCluster: true,
			shardOverrides: []ShardOverride{
				{ShardNames: []string{"foo-0"}, MemberConfig: defaultMemberConfig},
				{ShardNames: []string{"foo-1"}, Members: ptr.To(2)},
				{ShardNames: []string{"foo-2"}, StatefulSetConfiguration: &v1.StatefulSetConfiguration{}},
			},
			expectWarning: true,
			expectedWarnings: []status.Warning{
				"spec.shardOverrides.memberConfig is ignored in Multi Cluster topology. Use instead: spec.shardOverrides.clusterSpecList.memberConfig",
				"spec.shardOverrides.members is ignored in Multi Cluster topology. Use instead: spec.shardOverrides.clusterSpecList.members",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sc *MongoDB
			if tt.isMultiCluster {
				sc = NewDefaultMultiShardedClusterBuilder().SetName("foo").Build()
			} else {
				sc = NewDefaultShardedClusterBuilder().SetName("foo").Build()
			}

			sc.Spec.MongodsPerShardCount = tt.mongodsPerShard
			sc.Spec.ConfigServerCount = tt.configServerCount
			sc.Spec.MongosCount = tt.mongosCount
			sc.Spec.ShardOverrides = tt.shardOverrides
			// To avoid validation error
			sc.Spec.ShardCount = len(tt.shardOverrides)

			_, err := sc.ValidateCreate()
			// In case there is a validation error, we cannot expect warnings as well, the validation will stop with
			// the error
			if tt.expectError {
				require.Error(t, err)
				assert.Equal(t, tt.errorMessage, err.Error())
			} else {
				assert.NoError(t, err)
			}
			if tt.expectWarning {
				require.NotEmpty(t, sc.Status.Warnings)
				for _, expectedWarning := range tt.expectedWarnings {
					assertWarningExists(t, sc.Status.Warnings, expectedWarning)
				}
			} else {
				assert.Empty(t, sc.Status.Warnings)
			}
		})
	}
}

func TestDuplicateServiceObjectsIsIgnoredInSingleCluster(t *testing.T) {
	sc := NewDefaultShardedClusterBuilder().Build()
	truePointer := ptr.To(true)
	sc.Spec.DuplicateServiceObjects = truePointer
	_, err := sc.ValidateCreate()
	assert.NoError(t, err)
	assertWarningExists(t, sc.Status.Warnings, "In Single Cluster topology, spec.duplicateServiceObjects field is ignored")
}

func TestEmptyClusterSpecListInOverrides(t *testing.T) {
	sc := NewDefaultShardedClusterBuilder().SetShardCountSpec(1).Build()
	sc.Spec.ShardOverrides = []ShardOverride{
		{
			ShardNames: []string{fmt.Sprintf("%s-0", sc.Name)},
			ShardedClusterComponentOverrideSpec: ShardedClusterComponentOverrideSpec{
				ClusterSpecList: []ClusterSpecItemOverride{{ClusterName: "test-cluster"}},
			},
		},
	}
	_, err := sc.ValidateCreate()
	require.Error(t, err)
	assert.Equal(t, "cluster spec list in spec.shardOverrides must be empty in Single Cluster topology", err.Error())
}

func assertWarningExists(t *testing.T, warnings status.Warnings, expectedWarning status.Warning) {
	assert.NotEmpty(t, warnings)

	// Check if the expected warning exists in the warnings, either with or without a semicolon
	var found bool
	for _, warning := range warnings {
		if warning == expectedWarning || warning == expectedWarning+status.SEP {
			found = true
			break
		}
	}

	// If not found, print the list of warnings and fail the test
	if !found {
		assert.Fail(t, "Expected warning not found", "Expected warning: %q, but it was not found in warnings: %v", expectedWarning, warnings)
	}
}

func TestValidateShardName(t *testing.T) {
	// Example usage
	resourceName := "foo"
	shardCount := 5

	tests := []struct {
		shardName string
		expect    bool
	}{
		{
			shardName: "foo-0",
			expect:    true,
		},
		{
			shardName: "foo-3",
			expect:    true,
		},
		{
			shardName: "foo-5",
			expect:    false,
		},
		{
			shardName: "foo-01",
			expect:    false,
		},
		{
			shardName: "foo2",
			expect:    false,
		},
		{
			shardName: "bar-2",
			expect:    false,
		},
		{
			shardName: "foo-a",
			expect:    false,
		},
		{
			shardName: "foo-2-2",
			expect:    false,
		},
		{
			shardName: "",
			expect:    false,
		},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("name %s", tt.shardName), func(t *testing.T) {
			assert.Equal(t, tt.expect, validateShardName(tt.shardName, shardCount, resourceName))
		})
	}
}

func TestNoTopologyMigration(t *testing.T) {
	scSingle := NewDefaultShardedClusterBuilder().Build()
	scMulti := NewDefaultShardedClusterBuilder().SetMultiClusterTopology().Build()
	_, err := scSingle.ValidateUpdate(scMulti)
	require.Error(t, err)
	assert.Equal(t, "Automatic Topology Migration (Single/Multi Cluster) is not supported for MongoDB resource", err.Error())
}

func TestValidateMemberClusterIsSubsetOfKubeConfig(t *testing.T) {
	testCases := []struct {
		name           string
		clusterSpec    ClusterSpecList
		shardOverrides []ShardOverride
		expectedError  bool
		expectedErrMsg string
	}{
		{
			name: "Failure due to mismatched clusters",
			clusterSpec: ClusterSpecList{
				{ClusterName: "hello", Members: 1},
				{ClusterName: "hi", Members: 2},
			},
			expectedError:  true,
			expectedErrMsg: "Error when validating spec.shardSpec ClusterSpecList: The following clusters specified in ClusterSpecList is not present in Kubeconfig: [hello hi], instead - the following are: [foo bar]",
		},
		{
			name: "Success when clusters match",
			clusterSpec: ClusterSpecList{
				{ClusterName: "foo", Members: 1},
			},
			expectedError: false,
		},
		{
			name: "Failure with partial mismatch of clusters",
			clusterSpec: ClusterSpecList{
				{ClusterName: "foo", Members: 1},
				{ClusterName: "unknown", Members: 2},
			},
			expectedError:  true,
			expectedErrMsg: "Error when validating spec.shardSpec ClusterSpecList: The following clusters specified in ClusterSpecList is not present in Kubeconfig: [unknown], instead - the following are: [foo bar]",
		},
		{
			name: "Success with multiple clusters in KubeConfig",
			clusterSpec: ClusterSpecList{
				{ClusterName: "foo", Members: 1},
				{ClusterName: "bar", Members: 2},
			},
			expectedError: false,
		},
		{
			name: "Success with multiple clusters in shard overrides",
			clusterSpec: ClusterSpecList{
				{ClusterName: "foo", Members: 1},
				{ClusterName: "bar", Members: 2},
			},
			shardOverrides: []ShardOverride{
				{
					ShardNames: []string{"foo-0"},
					ShardedClusterComponentOverrideSpec: ShardedClusterComponentOverrideSpec{
						ClusterSpecList: []ClusterSpecItemOverride{{ClusterName: "foo"}, {ClusterName: "bar"}},
					},
				},
				{
					ShardNames: []string{"foo-1", "foo-2"},
					ShardedClusterComponentOverrideSpec: ShardedClusterComponentOverrideSpec{
						ClusterSpecList: []ClusterSpecItemOverride{{ClusterName: "foo"}},
					},
				},
			},
			expectedError: false,
		},
		{
			name: "Error with incorrect clusters in shard overrides",
			clusterSpec: ClusterSpecList{
				{ClusterName: "foo", Members: 1},
				{ClusterName: "bar", Members: 2},
			},
			shardOverrides: []ShardOverride{
				{
					ShardNames: []string{"foo-0"},
					ShardedClusterComponentOverrideSpec: ShardedClusterComponentOverrideSpec{
						ClusterSpecList: []ClusterSpecItemOverride{{ClusterName: "foo"}, {ClusterName: "unknown"}},
					},
				},
				{
					ShardNames: []string{"foo-1", "foo-2"},
					ShardedClusterComponentOverrideSpec: ShardedClusterComponentOverrideSpec{
						ClusterSpecList: []ClusterSpecItemOverride{{ClusterName: "foo"}},
					},
				},
			},
			expectedError:  true,
			expectedErrMsg: "Error when validating shard [foo-0] override ClusterSpecList: The following clusters specified in ClusterSpecList is not present in Kubeconfig: [unknown], instead - the following are: [foo bar]",
		},
	}

	// Run each test case
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			// The below function will create a temporary file and set the correct environment variable, so that
			// the validation checking if clusters belong to the KubeConfig find this file
			file := createTestKubeConfigAndSetEnvMultipleClusters(t)
			defer os.Remove(file.Name())

			sc := NewDefaultMultiShardedClusterBuilder().
				SetName("foo").
				SetShardCountSpec(3).
				SetAllClusterSpecLists(tt.clusterSpec).
				SetShardOverrides(tt.shardOverrides).
				Build()
			_, err := sc.ValidateCreate()

			if tt.expectedError {
				require.Error(t, err)
				assert.Equal(t, tt.expectedErrMsg, err.Error())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TODO: partially duplicated from mongodbmulti_validation_test.go, consider moving to another file
// Helper function to create a KubeConfig with multiple clusters
func createTestKubeConfigAndSetEnvMultipleClusters(t *testing.T) *os.File {
	//nolint
	testKubeConfig := fmt.Sprintf(`
apiVersion: v1
contexts:
- context:
    cluster: foo
    namespace: a-1661872869-pq35wlt3zzz
    user: foo
  name: foo
- context:
    cluster: bar
    namespace: b-1661872869-pq35wlt3yyy
    user: bar
  name: bar
kind: Config
users:
- name: foo
  user:
    token: eyJhbGciOi
- name: bar
  user:
    token: eyJhbGciOi
`)

	file, err := os.CreateTemp("", "kubeconfig")
	assert.NoError(t, err)
	_, err = file.WriteString(testKubeConfig)
	assert.NoError(t, err)
	t.Setenv(multicluster.KubeConfigPathEnv, file.Name())
	return file
}

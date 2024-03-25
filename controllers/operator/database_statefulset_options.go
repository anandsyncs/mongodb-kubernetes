package operator

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
)

// CurrentAgentAuthMechanism will assign the given value as the current authentication mechanism.
func CurrentAgentAuthMechanism(mode string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.CurrentAgentAuthMode = mode
	}
}

// PodEnvVars will assign the given env vars which will used during StatefulSet construction.
func PodEnvVars(vars *env.PodEnvVars) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.PodVars = vars
	}
}

// Replicas will set the given number of replicas when building a StatefulSet.
func Replicas(replicas int) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.Replicas = replicas
	}
}

// CertificateHash will assign the given CertificateHash during StatefulSet construction.
func CertificateHash(hash string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.CertificateHash = hash
	}
}

// InternalClusterHash will assign the given InternalClusterHash during StatefulSet construction.
func InternalClusterHash(hash string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.InternalClusterHash = hash
	}
}

func PrometheusTLSCertHash(hash string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.PrometheusTLSCertHash = hash
	}
}

// WithLabels will assing the provided labels during the statefulset construction
func WithLabels(labels map[string]string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.Labels = labels
	}
}

// WithVaultConfig sets the vault configuration to extract annotations for the statefulset.
func WithVaultConfig(config vault.VaultConfiguration) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.VaultConfig = config
	}
}

func WithAdditionalMongodConfig(additionalMongodConfig *mdbv1.AdditionalMongodConfig) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.AdditionalMongodConfig = additionalMongodConfig
	}
}

func WithAgentVersion(agentVersion string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.AutomationAgentVersion = agentVersion
	}
}

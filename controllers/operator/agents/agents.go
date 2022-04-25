package agents

import (
	"errors"
	"fmt"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SecretGetCreator interface {
	secret.Getter
	secret.Creator
}

type retryParams struct {
	waitSeconds int
	retrials    int
}

// ensureAgentKeySecretExists checks if the Secret with specified name (<groupId>-group-secret) exists, otherwise tries to
// generate agent key using OM public API and create Secret containing this key. Generation of a key is expected to be
// a rare operation as the group creation api generates agent key already (so the only possible situation is when the group
// was created externally and agent key wasn't generated before)
// Returns the api key existing/generated
func EnsureAgentKeySecretExists(secretGetCreator secrets.SecretClient, agentKeyGenerator om.AgentKeyGenerator, namespace, agentKey, projectId string, basePath string, log *zap.SugaredLogger) error {
	secretName := ApiKeySecretName(projectId)
	log = log.With("secret", secretName)
	_, err := secretGetCreator.GetSecret(kube.ObjectKey(namespace, secretName))
	if err != nil {
		if agentKey == "" {
			log.Info("Generating agent key as current project doesn't have it")

			agentKey, err = agentKeyGenerator.GenerateAgentKey()
			if err != nil {
				return fmt.Errorf("failed to generate agent key in OM: %s", err)
			}
			log.Info("Agent key was successfully generated")
		}

		data := map[string]interface{}{
			"data": map[string]interface{}{
				util.OmAgentApiKey: agentKey,
			},
		}

		if vault.IsVaultSecretBackend() {
			// we only want to create secret if it doesn't exist in vault
			APIKeyPath := fmt.Sprintf("%s/%s/%s", basePath, namespace, secretName)
			_, err := secretGetCreator.VaultClient.ReadSecretBytes(APIKeyPath)
			if err != nil && secrets.SecretNotExist(err) {
				err = secretGetCreator.VaultClient.PutSecret(APIKeyPath, data)
				if err != nil {
					return fmt.Errorf("failed to create AgentKey secret in vault: %s", err)
				}
				log.Infof("Project agent key is saved in Vault")
				return nil
			}
			return err
		}

		// todo pass a real owner in a next PR
		if err = createAgentKeySecret(secretGetCreator, kube.ObjectKey(namespace, secretName), agentKey, nil); err != nil {
			if apiErrors.IsAlreadyExists(err) {
				return nil
			}
			return fmt.Errorf("failed to create Secret: %s", err)
		}
		log.Infof("Project agent key is saved in Kubernetes Secret for later usage")
		return nil
	}

	return nil
}

// ApiKeySecretName for a given ProjectID (`project`) returns the name of
// the secret associated with it.
func ApiKeySecretName(project string) string {
	return fmt.Sprintf("%s-group-secret", project)
}

func WaitForRsAgentsToRegister(set appsv1.StatefulSet, clusterName string, omConnection om.Connection, log *zap.SugaredLogger) error {
	return WaitForRsAgentsToRegisterReplicasSpecified(set, 0, clusterName, omConnection, log)
}

// WaitForRsAgentsToRegister waits until all the agents associated with the given StatefulSet have registered with Ops Manager.
func WaitForRsAgentsToRegisterReplicasSpecified(set appsv1.StatefulSet, members int, clusterName string, omConnection om.Connection, log *zap.SugaredLogger) error {
	hostnames, _ := dns.GetDnsForStatefulSetReplicasSpecified(set, clusterName, members)
	log = log.With("statefulset", set.Name)

	if !waitUntilRegistered(omConnection, log, retryParams{retrials: 5, waitSeconds: 3}, hostnames...) {
		return errors.New("some agents failed to register or the Operator is using the wrong host names for the pods. " +
			"Make sure the 'spec.clusterDomain' is set if it's different from the default Kubernetes cluster " +
			"name ('cluster.local') ")
	}
	return nil
}

// WaitForRsAgentsToRegisterReplicasSpecifiedMultiCluster waits for the specified agents to registry with Ops Manager.
func WaitForRsAgentsToRegisterReplicasSpecifiedMultiCluster(omConnection om.Connection, hostnames []string, log *zap.SugaredLogger) error {
	if !waitUntilRegistered(omConnection, log, retryParams{retrials: 10, waitSeconds: 9}, hostnames...) {
		return errors.New("some agents failed to register or the Operator is using the wrong host names for the pods. " +
			"Make sure the 'spec.clusterDomain' is set if it's different from the default Kubernetes cluster " +
			"name ('cluster.local') ")
	}
	return nil
}

// waitUntilRegistered waits until all agents with 'agentHostnames' are registered in OM. Note, that wait
// happens after retrial - this allows to skip waiting in case agents are already registered
func waitUntilRegistered(omConnection om.Connection, log *zap.SugaredLogger, r retryParams, agentHostnames ...string) bool {
	log.Infow("Waiting for agents to register with OM", "agent hosts", agentHostnames)
	// environment variables are used only for tests
	waitSeconds := env.ReadIntOrDefault(util.PodWaitSecondsEnv, r.waitSeconds)
	retrials := env.ReadIntOrDefault(util.PodWaitRetriesEnv, r.retrials)

	agentsCheckFunc := func() (string, bool) {
		registeredCount := 0
		found, err := om.TraversePages(
			omConnection.ReadAutomationAgents,
			func(aa interface{}) bool {
				automationAgent := aa.(om.AgentStatus)

				for _, hostname := range agentHostnames {
					if automationAgent.IsRegistered(hostname, log) {
						registeredCount++
						if registeredCount == len(agentHostnames) {
							return true
						}
					}
				}
				return false
			},
		)

		if err != nil {
			log.Errorw("Received error when reading automation agent pages", "err", err)
		}

		var msg string
		if registeredCount == 0 {
			msg = fmt.Sprintf("None of %d agents has registered with OM", len(agentHostnames))
		} else {
			msg = fmt.Sprintf("Only %d of %d agents have registered with OM", registeredCount, len(agentHostnames))
		}
		return msg, found
	}

	return util.DoAndRetry(agentsCheckFunc, log, retrials, waitSeconds)
}

func createAgentKeySecret(secretCreator secrets.SecretClient, objectKey client.ObjectKey, agentKey string, owner v1.CustomResourceReadWriter) error {
	agentKeySecret := secret.Builder().
		SetField(util.OmAgentApiKey, agentKey).
		SetOwnerReferences(kube.BaseOwnerReference(owner)).
		SetName(objectKey.Name).
		SetNamespace(objectKey.Namespace).
		Build()
	return secretCreator.KubeClient.CreateSecret(agentKeySecret)
}

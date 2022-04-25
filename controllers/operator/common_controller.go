package operator

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"

	"github.com/10gen/ops-manager-kubernetes/pkg/passwordhash"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	"encoding/pem"
	"fmt"
	"strings"
	"time"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/tls"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/inspect"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/blang/semver"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func automationConfigFirstMsg(resourceType string, valueToSet string) string {
	return fmt.Sprintf("About to set `%s` to %s. automationConfig needs to be updated first", resourceType, valueToSet)
}

type patchValue struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value"`
}

// ReconcileCommonController is the "parent" controller that is included into each specific controller and allows
// to reuse the common functionality
type ReconcileCommonController struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	scheme *runtime.Scheme
	client kubernetesClient.Client
	secrets.SecretClient

	watch.ResourceWatcher
}

func newReconcileCommonController(mgr manager.Manager) *ReconcileCommonController {
	newClient := kubernetesClient.NewClient(mgr.GetClient())
	var vaultClient *vault.VaultClient

	if vault.IsVaultSecretBackend() {
		var err error
		// creates the in-cluster config, we cannot use the controller-runtime manager client
		// since the manager hasn't been started yet. Using it will cause error "the cache is not started, can not read objects"
		config, err := rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			panic(err.Error())
		}
		vaultClient, err = vault.InitVaultClient(clientset)
		if err != nil {
			panic(fmt.Sprintf("Can not initialize vault client: %s", err))
		}
		if err := vaultClient.Login(); err != nil {
			panic(fmt.Errorf("unable to log in with vault client: %s", err))
		}
	}
	return &ReconcileCommonController{
		client: newClient,
		SecretClient: secrets.SecretClient{
			VaultClient: vaultClient,
			KubeClient:  newClient,
		},
		scheme:          mgr.GetScheme(),
		ResourceWatcher: watch.NewResourceWatcher(),
	}
}

func ensureRoles(roles []mdbv1.MongoDbRole, conn om.Connection, log *zap.SugaredLogger) workflow.Status {
	d, err := conn.ReadDeployment()
	if err != nil {
		return workflow.Failed(err.Error())
	}
	dRoles := d.GetRoles()
	if reflect.DeepEqual(dRoles, roles) {
		return workflow.OK()
	}
	// HELP-20798: the agent deals correctly with a null value for
	// privileges only when creating a role, not when updating
	// we work around it by explicitly passing empty array
	for i, role := range roles {
		if role.Privileges == nil {
			roles[i].Privileges = []mdbv1.Privilege{}
		}
	}
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			d.SetRoles(roles)
			return nil
		},
		log,
	)
	if err != nil {
		return workflow.Failed(err.Error())
	}
	return workflow.OK()
}

// updateStatus updates the status for the CR using patch operation. Note, that the resource status is mutated and
// it's important to pass resource by pointer to all methods which invoke current 'updateStatus'.
func (c *ReconcileCommonController) updateStatus(reconciledResource v1.CustomResourceReadWriter, status workflow.Status, log *zap.SugaredLogger, statusOptions ...status.Option) (reconcile.Result, error) {
	status.Log(log)

	mergedOptions := append(statusOptions, status.StatusOptions()...)
	reconciledResource.UpdateStatus(status.Phase(), mergedOptions...)
	if err := c.patchUpdateStatus(reconciledResource, statusOptions...); err != nil {
		log.Errorf("Error updating status to %s: %s", status.Phase(), err)
		return reconcile.Result{}, err
	}
	return status.ReconcileResult()
}

// We fetch a fresh version in case any modifications have been made.
// Note, that this method enforces update ONLY to the status, so the reconciliation events happening because of this
// can be filtered out by 'controller.shouldReconcile'
// The "jsonPatch" merge allows to update only status field
func (c *ReconcileCommonController) patchUpdateStatus(resource v1.CustomResourceReadWriter, options ...status.Option) error {
	payload := []patchValue{{
		Op:   "replace",
		Path: resource.GetStatusPath(options...),
		// in most cases this will be "/status", but for each of the different Ops Manager components
		// this will be different
		Value: resource.GetStatus(options...),
	}}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	patch := client.RawPatch(types.JSONPatchType, data)
	err = c.client.Status().Patch(context.TODO(), resource, patch)

	if err != nil && apiErrors.IsInvalid(err) {
		zap.S().Debug("The Status subresource might not exist yet, creating empty subresource")
		if err := c.ensureStatusSubresourceExists(resource, options...); err != nil {
			zap.S().Debug("Error from ensuring status subresource: %s", err)
			return err
		}
		return c.client.Status().Patch(context.TODO(), resource, patch)
	}

	return nil
}

type emptyPayload struct{}

// ensureStatusSubresourceExists ensures that the status subresource section we are trying to write to exists.
// if we just try and patch the full path directly, the subresource sections are not recursively created, so
// we need to ensure that the actual object we're trying to write to exists, otherwise we will get errors.
func (c *ReconcileCommonController) ensureStatusSubresourceExists(resource v1.CustomResourceReadWriter, options ...status.Option) error {
	fullPath := resource.GetStatusPath(options...)
	parts := strings.Split(fullPath, "/")

	if strings.HasPrefix(fullPath, "/") {
		parts = parts[1:]
	}

	var path []string
	for _, part := range parts {
		pathStr := "/" + strings.Join(path, "/")
		path = append(path, part)
		emptyPatchPayload := []patchValue{{
			Op:    "add",
			Path:  pathStr,
			Value: emptyPayload{},
		}}
		data, err := json.Marshal(emptyPatchPayload)
		if err != nil {
			return err
		}
		patch := client.RawPatch(types.JSONPatchType, data)
		if err := c.client.Status().Patch(context.TODO(), resource, patch); err != nil && !apiErrors.IsInvalid(err) {
			return err
		}
	}
	return nil
}

// getResource populates the provided runtime.Object with some additional error handling
func (c *ReconcileCommonController) getResource(request reconcile.Request, resource v1.CustomResourceReadWriter, log *zap.SugaredLogger) (reconcile.Result, error) {
	err := c.client.Get(context.TODO(), request.NamespacedName, resource)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			log.Debugf("Object %s doesn't exist, was it deleted after reconcile request?", request.NamespacedName)
			return reconcile.Result{}, err
		}
		// Error reading the object - requeue the request.
		log.Errorf("Failed to query object %s: %s", request.NamespacedName, err)
		return reconcile.Result{RequeueAfter: 10 * time.Second}, err
	}
	return reconcile.Result{}, nil
}

// prepareResourceForReconciliation finds the object being reconciled. Returns the reconcile result and any error that
// occurred.
func (c *ReconcileCommonController) prepareResourceForReconciliation(
	request reconcile.Request, resource v1.CustomResourceReadWriter, log *zap.SugaredLogger) (reconcile.Result, error) {
	if result, err := c.getResource(request, resource, log); err != nil {
		return result, err
	}

	result, err := c.updateStatus(resource, workflow.Reconciling(), log)
	if err != nil {
		return result, err
	}

	// Reset warnings so that they are not stale, will populate accurate warnings in reconciliation
	resource.SetWarnings([]status.Warning{})

	return reconcile.Result{}, nil
}

// checkIfHasExcessProcesses will check if the project has excess processes.
// Also it removes the tag ExternallyManaged from the project in this case as
// the user may need to clean the resources from OM UI if they move the
// resource to another project (as recommended by the migration instructions).
func checkIfHasExcessProcesses(conn om.Connection, resource *mdbv1.MongoDB, log *zap.SugaredLogger) workflow.Status {
	deployment, err := conn.ReadDeployment()
	if err != nil {
		return workflow.Failed(err.Error())
	}
	excessProcesses := deployment.GetNumberOfExcessProcesses(resource.Name)
	if excessProcesses == 0 {
		// cluster is empty or this resource is the only one living on it
		return workflow.OK()
	}
	// remove tags if multiple clusters in project
	groupWithTags := &om.Project{
		Name:  conn.GroupName(),
		OrgID: conn.OrgID(),
		ID:    conn.GroupID(),
		Tags:  []string{},
	}
	_, err = conn.UpdateProject(groupWithTags)
	if err != nil {
		log.Warnw("could not remove externally managed tag from Ops Manager group", "error", err)
	}

	return workflow.Pending("cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes-operator/stable/tutorial/migrate-to-single-resource/)")
}

// validateInternalClusterCertsAndCheckTLSType verifies that all the x509 internal cluster certs exist and return whether they are built following the kubernetes.io/tls secret type (tls.crt/tls.key entries).
// TODO: this is almost the same as certs.EnsureSSLCertsForStatefulSet, we should centralize the functionality
func (r *ReconcileCommonController) validateInternalClusterCertsAndCheckTLSType(configurator certs.X509CertConfigurator, opts certs.Options, log *zap.SugaredLogger) (bool, error) {

	secretName := opts.InternalClusterSecretName

	err, newTLSDesign := certs.VerifyAndEnsureCertificatesForStatefulSet(
		configurator.GetSecretReadClient(),
		configurator.GetSecretWriteClient(),
		secretName, opts, log)
	if err != nil {
		return true, fmt.Errorf("the secret object '%s' does not contain all the certificates needed: %s", secretName, err)
	}

	if newTLSDesign {
		secretName = fmt.Sprintf("%s%s", secretName, certs.OperatorGeneratedCertSuffix)
	}

	// Validates that the secret is valid
	if err := certs.ValidateCertificates(r.client, secretName, opts.Namespace); err != nil {
		return false, err
	}
	return newTLSDesign, nil
}

// ensureBackupConfigurationAndUpdateStatus configures backup in Ops Manager based on the MongoDB resources spec
func (r *ReconcileCommonController) ensureBackupConfigurationAndUpdateStatus(conn om.Connection, mdb backup.ConfigReaderUpdater, log *zap.SugaredLogger) workflow.Status {
	statusOpt, opts := backup.EnsureBackupConfigurationInOpsManager(mdb, conn.GroupID(), conn, log)
	if len(opts) > 0 {
		if _, err := r.updateStatus(mdb, statusOpt, log, opts...); err != nil {
			return workflow.Failed(err.Error())
		}
	}
	return statusOpt
}

// validateMongoDBResource performs validation on the MongoDBResource
func validateMongoDBResource(mdb *mdbv1.MongoDB, conn om.Connection) workflow.Status {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return workflow.Failed(err.Error())
	}

	if status := validateScram(mdb, ac); !status.IsOK() {
		return status
	}

	return workflow.OK()
}

func ensureSupportedOpsManagerVersion(conn om.Connection) workflow.Status {
	omVersionString := conn.OpsManagerVersion()
	if !omVersionString.IsCloudManager() {
		omVersion, err := omVersionString.Semver()
		if err != nil {
			return workflow.Failed("Failed when trying to parse Ops Manager version")
		}
		if omVersion.LT(semver.MustParse(oldestSupportedOpsManagerVersion)) {
			return workflow.Unsupported("This MongoDB ReplicaSet is managed by Ops Manager version %s, which is not supported by this version of the operator. Please upgrade it to a version >=%s", omVersion, oldestSupportedOpsManagerVersion)

		}
	}
	return workflow.OK()
}

// scaleStatefulSet sets the number of replicas for a StatefulSet and returns a reference of the updated resource.
func (r *ReconcileCommonController) scaleStatefulSet(namespace, name string, replicas int32) (appsv1.StatefulSet, error) {
	if set, err := r.client.GetStatefulSet(kube.ObjectKey(namespace, name)); err != nil {
		return set, err
	} else {
		set.Spec.Replicas = &replicas
		return r.client.UpdateStatefulSet(set)
	}

}

// getStatefulSetStatus returns the workflow.Status based on the status of the StatefulSet.
// If the StatefulSet is not ready the request will be retried in 3 seconds (instead of the default 10 seconds)
// allowing to reach "ready" status sooner
func getStatefulSetStatus(namespace, name string, client kubernetesClient.Client) workflow.Status {
	set, err := client.GetStatefulSet(kube.ObjectKey(namespace, name))
	i := 0

	// Sometimes it is possible that the StatefulSet which has just been created
	// returns a not found error when getting it too soon afterwards.
	for apiErrors.IsNotFound(err) && i < 10 {
		i++
		zap.S().Debugf("StatefulSet was not found: %s, attempt %d", err, i)
		time.Sleep(time.Second * 1)
		set, err = client.GetStatefulSet(kube.ObjectKey(namespace, name))
	}

	if err != nil {
		return workflow.Failed(err.Error())
	}

	if statefulSetState := inspect.StatefulSet(set); !statefulSetState.IsReady() {
		return workflow.
			Pending(statefulSetState.GetMessage()).
			WithResourcesNotReady(statefulSetState.GetResourcesNotReadyStatus()).
			WithRetry(3)
	}
	return workflow.OK()
}

// validateScram ensures that the SCRAM configuration is valid for the MongoDBResource
func validateScram(mdb *mdbv1.MongoDB, ac *om.AutomationConfig) workflow.Status {
	specVersion, err := semver.Make(util.StripEnt(mdb.Spec.GetMongoDBVersion()))
	if err != nil {
		return workflow.Failed(err.Error())
	}

	scram256IsAlreadyEnabled := stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(authentication.ScramSha256))
	attemptingToDowngradeMongoDBVersion := ac.Deployment.MinimumMajorVersion() >= 4 && specVersion.Major < 4
	isDowngradingFromScramSha256ToScramSha1 := attemptingToDowngradeMongoDBVersion && stringutil.Contains(mdb.Spec.Security.Authentication.GetModes(), "SCRAM") && scram256IsAlreadyEnabled

	if isDowngradingFromScramSha256ToScramSha1 {
		return workflow.Invalid("Unable to downgrade to SCRAM-SHA-1 when SCRAM-SHA-256 has been enabled")
	}

	return workflow.OK()
}

// Use the first "CERTIFICATE" block found in the PEM file.
func getSubjectFromCertificate(cert string) (string, error) {
	block, rest := pem.Decode([]byte(cert))
	if block != nil && block.Type == "CERTIFICATE" {
		subjects, _, err := authentication.GetCertificateSubject(cert)
		if err != nil {
			return "", err
		}
		return subjects, nil
	}
	if len(rest) > 0 {
		subjects, _, err := authentication.GetCertificateSubject(string(rest))
		if err != nil {
			return "", err
		}
		return subjects, nil
	}
	return "", fmt.Errorf("unable to extract the subject line from the provided certificate")
}

// updateOmAuthentication examines the state of Ops Manager and the desired state of the MongoDB resource and
// enables/disables authentication. If the authentication can't be fully configured, a boolean value indicating that
// an additional reconciliation needs to be queued up to fully make the authentication changes is returned.
func (r *ReconcileCommonController) updateOmAuthentication(conn om.Connection, processNames []string, ar authentication.AuthResource, agentCertSecretName string, caFilepath string, log *zap.SugaredLogger) (status workflow.Status, multiStageReconciliation bool) {
	// don't touch authentication settings if resource has not been configured with them
	if ar.GetSecurity() == nil || ar.GetSecurity().Authentication == nil {
		return workflow.OK(), false
	}

	// we need to wait for all agents to be ready before configuring any authentication settings
	if err := om.WaitForReadyState(conn, processNames, log); err != nil {
		return workflow.Failed(err.Error()), false
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return workflow.Failed(err.Error()), false
	}

	clientCerts := util.OptionalClientCertficates
	if ar.GetSecurity().RequiresClientTLSAuthentication() {
		clientCerts = util.RequireClientCertificates
	}

	scramAgentUserName := util.AutomationAgentUserName
	// only use the default name if there is not already a configure user name
	if ac.Auth.AutoUser != "" && ac.Auth.AutoUser != scramAgentUserName {
		scramAgentUserName = ac.Auth.AutoUser
	}

	authOpts := authentication.Options{
		MinimumMajorVersion: ar.GetMinimumMajorVersion(),
		Mechanisms:          ar.GetSecurity().Authentication.Modes,
		ProcessNames:        processNames,
		AuthoritativeSet:    !ar.GetSecurity().Authentication.IgnoreUnknownUsers,
		AgentMechanism:      ar.GetSecurity().GetAgentMechanism(ac.Auth.AutoAuthMechanism),
		ClientCertificates:  clientCerts,
		AutoUser:            scramAgentUserName,
		AutoLdapGroupDN:     ar.GetSecurity().Authentication.Agents.AutomationLdapGroupDN,
		CAFilePath:          caFilepath,
	}
	var databaseSecretPath string
	if r.VaultClient != nil {
		databaseSecretPath = r.VaultClient.DatabaseSecretPath()
	}
	if ar.IsLDAPEnabled() {
		bindUserPassword, err := r.ReadSecretKey(kube.ObjectKey(ar.GetNamespace(), ar.GetSecurity().Authentication.Ldap.BindQuerySecretRef.Name), databaseSecretPath, "password")

		if err != nil {
			return workflow.Failed(fmt.Sprintf("error reading bind user password: %s", err)), false
		}

		caContents := ""
		ca := ar.GetSecurity().Authentication.Ldap.CAConfigMapRef
		if ca != nil {
			log.Debugf("Sending CA file to Pods via AutomationConfig: %s/%s/%s", ar.GetNamespace(), ca.Name, ca.Key)
			caContents, err = configmap.ReadKey(r.client, ca.Key, types.NamespacedName{Name: ca.Name, Namespace: ar.GetNamespace()})
			if err != nil {
				return workflow.Failed(fmt.Sprintf("error reading CA configmap: %s", err)), false
			}
		}

		authOpts.Ldap = ar.GetLDAP(bindUserPassword, caContents)
	}

	log.Debugf("Using authentication options %+v", authentication.Redact(authOpts))

	agentSecretSelector := ar.GetSecurity().AgentClientCertificateSecretName(ar.GetName())
	if agentCertSecretName != "" {
		agentSecretSelector.Name = agentCertSecretName
	}
	wantToEnableAuthentication := ar.GetSecurity().Authentication.Enabled
	if wantToEnableAuthentication && canConfigureAuthentication(ac, ar.GetSecurity().Authentication.GetModes(), log) {
		log.Info("Configuring authentication for MongoDB resource")

		if ar.GetSecurity().ShouldUseX509(ac.Auth.AutoAuthMechanism) || ar.GetSecurity().ShouldUseClientCertificates() {
			agentSecret := &corev1.Secret{}
			if err := r.client.Get(context.TODO(), kube.ObjectKey(ar.GetNamespace(), agentSecretSelector.Name), agentSecret); client.IgnoreNotFound(err) != nil {
				return workflow.Failed(err.Error()), false
			}
			// If the agent secret is of type TLS, we can find the certificate under the standard key,
			// otherwise the concatenated PEM secret would contain the certificate and keys under the selector's
			// Key identifying the agent. In single cluster workloads this path is working with the concatenated PEM secret,
			// while in multi cluster it is working with the TLS secret in the central cluster.
			if agentSecret.Type == corev1.SecretTypeTLS {
				agentSecretSelector.Key = corev1.TLSCertKey
			}

			authOpts, err = r.configureAgentSubjects(ar.GetNamespace(), agentSecretSelector, authOpts, log)
			if err != nil {
				return workflow.Failed("error configuring agent subjects: %v", err), false
			}
			authOpts.AgentsShouldUseClientAuthentication = ar.GetSecurity().ShouldUseClientCertificates()

		}
		if ar.GetSecurity().ShouldUseLDAP(ac.Auth.AutoAuthMechanism) {
			secretRef := ar.GetSecurity().Authentication.Agents.AutomationPasswordSecretRef
			autoConfigPassword, err := r.ReadSecretKey(kube.ObjectKey(ar.GetNamespace(), secretRef.Name), databaseSecretPath, secretRef.Key)
			if err != nil {
				return workflow.Failed(fmt.Sprintf("error reading automation agent  password: %s", err)), false
			}

			authOpts.AutoPwd = autoConfigPassword
			userOpts := authentication.UserOptions{}
			agentName := ar.GetSecurity().Authentication.Agents.AutomationUserName
			userOpts.AutomationSubject = agentName
			authOpts.UserOptions = userOpts
		}

		if err := authentication.Configure(conn, authOpts, log); err != nil {
			return workflow.Failed(err.Error()), false
		}
	} else if wantToEnableAuthentication {
		// The MongoDB resource has been configured with a type of authentication
		// but the current state in Ops Manager does not allow a direct transition. This will require
		// an additional reconciliation after a partial update to Ops Manager.
		log.Debug("Attempting to enable authentication, but Ops Manager state will not allow this")
		return workflow.OK(), true
	} else {
		agentSecret := &corev1.Secret{}
		if err := r.client.Get(context.TODO(), kube.ObjectKey(ar.GetNamespace(), agentSecretSelector.Name), agentSecret); client.IgnoreNotFound(err) != nil {
			return workflow.Failed(err.Error()), false
		}

		if agentSecret.Type == corev1.SecretTypeTLS {
			agentSecretSelector.Name = fmt.Sprintf("%s%s", agentSecretSelector.Name, certs.OperatorGeneratedCertSuffix)
		}

		// Should not fail if the Secret object with agent certs is not found.
		// It will only exist on x509 client auth enabled deployments.
		userOpts, err := r.readAgentSubjectsFromSecret(ar.GetNamespace(), agentSecretSelector, log)
		err = client.IgnoreNotFound(err)
		if err != nil {
			return workflow.Failed(err.Error()), true
		}

		authOpts.UserOptions = userOpts
		if err := authentication.Disable(conn, authOpts, false, log); err != nil {
			return workflow.Failed(err.Error()), false
		}
	}
	return workflow.OK(), false
}

// configureAgentSubjects returns a new authentication.Options which has configured the Subject lines for the automation agent.
// The Ops Manager user names for these agents will be configured based on the contents of the secret.
func (r *ReconcileCommonController) configureAgentSubjects(namespace string, secretKeySelector corev1.SecretKeySelector, authOpts authentication.Options, log *zap.SugaredLogger) (authentication.Options, error) {
	userOpts, err := r.readAgentSubjectsFromSecret(namespace, secretKeySelector, log)
	if err != nil {
		return authentication.Options{}, fmt.Errorf("error reading agent subjects from secret: %v", err)
	}
	authOpts.UserOptions = userOpts
	return authOpts, nil
}

func (r *ReconcileCommonController) readAgentSubjectsFromSecret(namespace string, secretKeySelector corev1.SecretKeySelector, log *zap.SugaredLogger) (authentication.UserOptions, error) {
	userOpts := authentication.UserOptions{}

	var databaseSecretPath string
	if r.VaultClient != nil {
		databaseSecretPath = r.VaultClient.DatabaseSecretPath()
	}
	agentCerts, err := r.ReadSecret(kube.ObjectKey(namespace, secretKeySelector.Name), databaseSecretPath)
	if err != nil {
		return userOpts, err
	}

	var automationAgentCert string
	var ok bool
	if automationAgentCert, ok = agentCerts[secretKeySelector.Key]; !ok {
		return userOpts, fmt.Errorf("could not find certificate with name %s", secretKeySelector.Key)
	}

	automationAgentSubject, err := getSubjectFromCertificate(automationAgentCert)
	if err != nil {
		return userOpts, fmt.Errorf("error extracting automation agent subject is not present %e", err)
	}

	log.Debugf("Automation certificate subject is %s", automationAgentSubject)

	return authentication.UserOptions{
		AutomationSubject: automationAgentSubject,
	}, nil
}

func (r *ReconcileCommonController) clearProjectAuthenticationSettings(conn om.Connection, mdb *mdbv1.MongoDB, processNames []string, log *zap.SugaredLogger) error {
	secretKeySelector := mdb.Spec.Security.AgentClientCertificateSecretName(mdb.Name)
	agentSecret := &corev1.Secret{}
	if err := r.client.Get(context.TODO(), kube.ObjectKey(mdb.Namespace, secretKeySelector.Name), agentSecret); client.IgnoreNotFound(err) != nil {
		return nil
	}

	if agentSecret.Type == corev1.SecretTypeTLS {
		secretKeySelector.Name = fmt.Sprintf("%s%s", secretKeySelector.Name, certs.OperatorGeneratedCertSuffix)
	}

	userOpts, err := r.readAgentSubjectsFromSecret(mdb.Namespace, secretKeySelector, log)
	err = client.IgnoreNotFound(err)
	if err != nil {
		return err
	}
	log.Infof("Disabling authentication for project: %s", conn.GroupName())
	disableOpts := authentication.Options{
		ProcessNames: processNames,
		UserOptions:  userOpts,
	}

	return authentication.Disable(conn, disableOpts, true, log)
}

// ensureX509SecretAndCheckTLSType checks if the secrets containing the certificates are present and whether the certificate are of kubernetes.io/tls type.
func (r *ReconcileCommonController) ensureX509SecretAndCheckTLSType(configurator certs.X509CertConfigurator, currentAuthMechanism string, log *zap.SugaredLogger) (workflow.Status, map[string]bool) {
	newTLSDesignMapping := map[string]bool{}
	authSpec := configurator.GetSecurity().Authentication
	if authSpec == nil || !configurator.GetSecurity().Authentication.Enabled {
		return workflow.OK(), newTLSDesignMapping
	}
	if configurator.GetSecurity().ShouldUseX509(currentAuthMechanism) {
		if !configurator.GetSecurity().IsTLSEnabled() {
			return workflow.Failed("Authentication mode for project is x509 but this MDB resource is not TLS enabled"), newTLSDesignMapping
		}
		agentSecretName := configurator.GetSecurity().AgentClientCertificateSecretName(configurator.GetName()).Name
		err, tlsFormat := certs.VerifyAndEnsureClientCertificatesForAgentsAndTLSType(
			configurator.GetSecretReadClient(), configurator.GetSecretWriteClient(),
			kube.ObjectKey(configurator.GetNamespace(), agentSecretName),
			log)
		if err != nil {
			return workflow.Failed(err.Error()), newTLSDesignMapping
		}

		newTLSDesignMapping[configurator.GetSecurity().AgentClientCertificateSecretName(configurator.GetName()).Name] = tlsFormat

	}

	if configurator.GetSecurity().GetInternalClusterAuthenticationMode() == util.X509 {
		errors := make([]error, 0)
		for _, certOption := range configurator.GetCertOptions() {
			newDesign, err := r.validateInternalClusterCertsAndCheckTLSType(configurator, certOption, log)
			if err != nil {
				errors = append(errors, err)
			}
			newTLSDesignMapping[certOption.InternalClusterSecretName] = newDesign
		}
		if len(errors) > 0 {
			return workflow.Failed("failed ensuring internal cluster authentication certs %s", errors[0]), newTLSDesignMapping
		}
	}
	return workflow.OK(), newTLSDesignMapping
}

// isPrometheusSupported checks if Prometheus integration can be enabled.
//
// Prometheus is only enabled in Cloud Manager and Ops Manager 5.9 (6.0) and above.
func isPrometheusSupported(conn om.Connection) bool {
	if conn.OpsManagerVersion().IsCloudManager() {
		return true
	}

	omVersion, err := conn.OpsManagerVersion().Semver()
	return err == nil && omVersion.GTE(semver.MustParse("5.9.0"))
}

// UpdatePrometheus configures Prometheus on the Deployment for this resource.
//
// This has been moved to this function so we can use it from Sharded Clusters and AppDB when I get to work on those resources.
func UpdatePrometheus(d *om.Deployment, conn om.Connection, mdb *mdbv1.MongoDB, sClient secrets.SecretClient, certName string, log *zap.SugaredLogger) error {
	if mdb.Spec.Prometheus == nil {
		return nil
	}

	if !isPrometheusSupported(conn) {
		log.Info("Prometheus can't be enabled, Prometheus is not supported in this version of Ops Manager")
		return nil
	}

	var err error
	var password string
	prometheus := mdb.Spec.Prometheus

	secretName := prometheus.PasswordSecretRef.Name
	if vault.IsVaultSecretBackend() {
		operatorSecretPath := sClient.VaultClient.OperatorSecretPath()
		passwordString := fmt.Sprintf("%s/%s/%s", operatorSecretPath, mdb.GetNamespace(), secretName)
		keyedPassword, err := sClient.VaultClient.ReadSecretString(passwordString)
		if err != nil {
			log.Infof("Prometheus can't be enabled, %s", err)
			return err
		}

		var ok bool
		password, ok = keyedPassword[prometheus.GetPasswordKey()]
		if !ok {
			errMsg := fmt.Sprintf("Prometheus password %s not in Secret %s", prometheus.GetPasswordKey(), passwordString)
			log.Info(errMsg)
			return fmt.Errorf(errMsg)
		}
	} else {
		secretNamespacedName := types.NamespacedName{Name: secretName, Namespace: mdb.Namespace}
		password, err = secret.ReadKey(sClient, prometheus.GetPasswordKey(), secretNamespacedName)
		if err != nil {
			log.Infof("Prometheus can't be enabled, %s", err)
			return err
		}
	}

	hash, salt := passwordhash.GenerateHashAndSaltForPassword(password)
	d.ConfigurePrometheus(prometheus, hash, salt, certName)

	return nil
}

// canConfigureAuthentication determines if based on the existing state of Ops Manager
// it is possible to configure the authentication mechanisms specified by the given MongoDB resource
// during this reconciliation. This function may return a different value on the next reconciliation
// if the state of Ops Manager has been changed.
func canConfigureAuthentication(ac *om.AutomationConfig, authenticationModes []string, log *zap.SugaredLogger) bool {
	attemptingToEnableX509 := !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option) && stringutil.Contains(authenticationModes, util.X509)
	canEnableX509InOpsManager := ac.Deployment.AllProcessesAreTLSEnabled() || ac.Deployment.NumberOfProcesses() == 0

	log.Debugw("canConfigureAuthentication",
		"attemptingToEnableX509", attemptingToEnableX509,
		"deploymentAuthMechanisms", ac.Auth.DeploymentAuthMechanisms,
		"modes", authenticationModes,
		"canEnableX509InOpsManager", canEnableX509InOpsManager,
		"allProcessesAreTLSEnabled", ac.Deployment.AllProcessesAreTLSEnabled(),
		"numberOfProcesses", ac.Deployment.NumberOfProcesses())

	if attemptingToEnableX509 {
		return canEnableX509InOpsManager
	}

	// x509 is the only mechanism with restrictions determined based on Ops Manager state
	return true
}

// newPodVars initializes a PodEnvVars instance based on the values of the provided Ops Manager connection, project config
// and connection spec
func newPodVars(conn om.Connection, projectConfig mdbv1.ProjectConfig, spec mdbv1.ConnectionSpec) *env.PodEnvVars {
	podVars := &env.PodEnvVars{}
	podVars.BaseURL = conn.BaseURL()
	podVars.ProjectID = conn.GroupID()
	podVars.User = conn.PublicKey()
	podVars.LogLevel = string(spec.LogLevel)
	podVars.SSLProjectConfig = projectConfig.SSLProjectConfig
	return podVars
}

func getVolumeFromStatefulSet(sts appsv1.StatefulSet, name string) (corev1.Volume, error) {
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if v.Name == name {
			return v, nil
		}
	}
	return corev1.Volume{}, fmt.Errorf("can't find volume %s in list of volumes: %v", name, sts.Spec.Template.Spec.Volumes)
}

func getVolumeMountFromMountLists(volumeMountsList []corev1.VolumeMount, name string) (corev1.VolumeMount, error) {
	for _, v := range volumeMountsList {
		if v.Name == name {
			return v, nil
		}
	}
	return corev1.VolumeMount{}, fmt.Errorf("can't find volumeMount %s in list of volumeMounts: %v", name, volumeMountsList)
}

func hasOldTLSDesign(volumeMounts []corev1.VolumeMount, volumeName string) bool {

	vMount, err := getVolumeMountFromMountLists(volumeMounts, volumeName)
	if err != nil {
		return false
	}

	return vMount.MountPath == util.SecretVolumeMountPath+"/certs" || vMount.MountPath == util.ConfigMapVolumeCAMountPath

}

// wasTLSSecretMounted checks whether or not TLS was previously enabled by looking at the state of the volumeMounts of the pod.
func wasTLSSecretMounted(secretGetter secret.Getter, currentSts appsv1.StatefulSet, volumeMounts []corev1.VolumeMount, mdb mdbv1.MongoDB, log *zap.SugaredLogger) bool {

	// If the volume has the "old-design" mount path, it means
	// that it was mounted when TLS was enabled
	if hasOldTLSDesign(volumeMounts, util.SecretVolumeName) {
		log.Debugf("Old design volume mount exists: TLS was enabled")
		return true
	}
	tlsVolume, err := getVolumeFromStatefulSet(currentSts, util.SecretVolumeName)
	if err != nil {
		return false
	}

	// With the new design, the volume is always mounted
	// But it is marked with optional.
	//
	// TLS was enabled if the secret it refers to is present

	secretName := tlsVolume.Secret.SecretName
	exists, err := secret.Exists(secretGetter, types.NamespacedName{
		Namespace: mdb.Namespace,
		Name:      secretName},
	)
	if err != nil {
		log.Warnf("can't determine whether the TLS certificate secret exists or not: %s. Will assume it doesn't", err)
		return false
	}
	log.Debugf("checking if secret %s exists: %v", secretName, exists)

	return exists

}

// wasCAConfigMapMounted checks whether or not the CA ConfigMap  by looking at the state of the volumeMounts of the pod.
func wasCAConfigMapMounted(configMapGetter configmap.Getter, currentSts appsv1.StatefulSet, volumeMounts []corev1.VolumeMount, mdb mdbv1.MongoDB, log *zap.SugaredLogger) bool {

	// If the volume has the "old-design" mount path, it means
	// that it was mounted when TLS was enabled
	if hasOldTLSDesign(volumeMounts, tls.ConfigMapVolumeCAName) {
		log.Debugf("Old design volume mount exists: TLS ConfigMap was mounted ")
		return true
	}
	caVolume, err := getVolumeFromStatefulSet(currentSts, util.ConfigMapVolumeCAMountPath)
	if err != nil {
		return false
	}

	// With the new design, the volume is always mounted
	// But it is marked with optional.
	//
	// The configMap was mounted if the configMap it refers to is present

	cmName := caVolume.ConfigMap.Name
	exists, err := configmap.Exists(configMapGetter, types.NamespacedName{
		Namespace: mdb.Namespace,
		Name:      cmName},
	)
	if err != nil {
		log.Warnf("can't determine whether the TLS ConfigMap exists or not: %s. Will assume it doesn't", err)
		return false
	}
	log.Debugf("checking if ConfigMap %s exists: %v", cmName, exists)

	return exists
}

type ConfigMapStatefulSetSecretGetter interface {
	statefulset.Getter
	secret.Getter
	configmap.Getter
}

// needToPublishStateFirst will check if the Published State of the StatfulSet backed MongoDB Deployments
// needs to be updated first. In the case of unmounting certs, for instance, the certs should be not
// required anymore before we unmount them, or the automation-agent and readiness probe will never
// reach goal state.
func needToPublishStateFirst(getter ConfigMapStatefulSetSecretGetter, mdb mdbv1.MongoDB, configFunc func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) bool {
	opts := configFunc(mdb)
	if opts.OldMemberCertSecret != "" {
		return false
	}
	namespacedName := kube.ObjectKey(mdb.Namespace, opts.Name)
	currentSts, err := getter.GetStatefulSet(namespacedName)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// No need to publish state as this is a new StatefulSet
			log.Debugf("New StatefulSet %s", namespacedName)
			return false
		}

		log.Debugw(fmt.Sprintf("Error getting StatefulSet %s", namespacedName), "error", err)
		return false
	}

	databaseContainer := container.GetByName(util.DatabaseContainerName, currentSts.Spec.Template.Spec.Containers)
	volumeMounts := databaseContainer.VolumeMounts

	if !mdb.Spec.Security.IsTLSEnabled() && wasTLSSecretMounted(getter, currentSts, volumeMounts, mdb, log) {
		log.Debug(automationConfigFirstMsg("security.tls.enabled", "false"))
		return true
	}

	if mdb.Spec.Security.TLSConfig.CA == "" && wasCAConfigMapMounted(getter, currentSts, volumeMounts, mdb, log) {
		log.Debug(automationConfigFirstMsg("security.tls.CA", "empty"))
		return true

	}

	if opts.PodVars.SSLMMSCAConfigMap == "" && statefulset.VolumeMountWithNameExists(volumeMounts, construct.CaCertName) {
		log.Debug(automationConfigFirstMsg("SSLMMSCAConfigMap", "empty"))
		return true
	}

	if mdb.Spec.Security.GetAgentMechanism(opts.CurrentAgentAuthMode) != util.X509 && statefulset.VolumeMountWithNameExists(volumeMounts, util.AgentSecretName) {
		log.Debug(automationConfigFirstMsg("project.AuthMode", "empty"))
		return true
	}

	if int32(opts.Replicas) < *currentSts.Spec.Replicas {
		log.Debug("Scaling down operation. automationConfig needs to be updated first")
		return true
	}

	return false
}

// completionMessage is just a general message printed in the logs after mongodb resource is created/updated
func completionMessage(url, projectID string) string {
	return fmt.Sprintf("Please check the link %s/v2/%s to see the status of the deployment", url, projectID)
}

// mongodbCleanUpOptions implements the required interface to be passed
// to the DeleteAllOf function, this cleans up resources of a given type with
// the provided labels in a specific namespace.
type mongodbCleanUpOptions struct {
	namespace string
	labels    map[string]string
}

func (m *mongodbCleanUpOptions) ApplyToDeleteAllOf(opts *client.DeleteAllOfOptions) {
	opts.Namespace = m.namespace
	opts.LabelSelector = labels.SelectorFromValidatedSet(m.labels)
}

// getAnnotationsForResource returns all of the annotations that should be applied to the resource
// at the end of the reconciliation. The additional mongod options must be manually
// set as the wrapper type we use prevents a regular `json.Marshal` from working in this case due to
// the `json "-"` tag.
func getAnnotationsForResource(mdb *mdbv1.MongoDB) (map[string]string, error) {
	finalAnnotations := make(map[string]string)
	specBytes, err := json.Marshal(mdb.Spec)
	if err != nil {
		return nil, err
	}
	finalAnnotations[util.LastAchievedSpec] = string(specBytes)

	switch mdb.Spec.ResourceType {
	case mdbv1.Standalone, mdbv1.ReplicaSet:
		additionalConfigBytes, err := json.Marshal(mdb.Spec.AdditionalMongodConfig.ToMap())
		if err != nil {
			return nil, err
		}
		finalAnnotations[util.LastAchievedMongodAdditionalOptions] = string(additionalConfigBytes)
	case mdbv1.ShardedCluster:
		if mdb.Spec.ShardSpec != nil {
			additionalShardBytes, err := json.Marshal(mdb.Spec.ShardSpec.AdditionalMongodConfig.ToMap())
			if err != nil {
				return nil, err
			}
			finalAnnotations[util.LastAchievedMongodAdditionalShardOptions] = string(additionalShardBytes)
		}

		if mdb.Spec.MongosSpec != nil {
			additionalMongosBytes, err := json.Marshal(mdb.Spec.MongosSpec.AdditionalMongodConfig.ToMap())
			if err != nil {
				return nil, err
			}
			finalAnnotations[util.LastAchievedMongodAdditionalMongosOptions] = string(additionalMongosBytes)
		}

		if mdb.Spec.ConfigSrvSpec != nil {
			additionalConfigServerBytes, err := json.Marshal(mdb.Spec.ConfigSrvSpec.AdditionalMongodConfig.ToMap())
			if err != nil {
				return nil, err
			}
			finalAnnotations[util.LastAchievedMongodAdditionalConfigServerOptions] = string(additionalConfigServerBytes)
		}

	}
	return finalAnnotations, nil
}

func oldTLSCertsAnnotations(oldTLSMemberCertSecret string) map[string]string {
	annotation := make(map[string]string)
	if oldTLSMemberCertSecret != "" {
		annotation[util.OldMemberCerts] = oldTLSMemberCertSecret
	}
	return annotation
}

func (r *ReconcileCommonController) getOldMemberCertSecret(rs *mdbv1.MongoDB, resourceName string) (string, error) {
	lastSpec, err := rs.GetLastSpec()
	if err != nil || lastSpec == nil {
		return "", err
	}

	oldSts, err := r.client.GetStatefulSet(types.NamespacedName{Name: resourceName, Namespace: rs.GetNamespace()})
	if err != nil {
		return "", err
	}
	resourceMemberCertSecret := lastSpec.GetSecurity().MemberCertificateSecretName(resourceName)
	if annotatedMemberCert, ok := rs.Annotations[util.OldMemberCerts]; ok {
		return annotatedMemberCert, nil
	} else if isSecretMounted(resourceMemberCertSecret, oldSts) {
		return resourceMemberCertSecret, nil
	}

	return "", nil
}

func isSecretMounted(secretName string, sts appsv1.StatefulSet) bool {
	for _, volume := range sts.Spec.Template.Spec.Volumes {
		if volume.Secret != nil {
			if volume.Secret.SecretName == secretName {
				return true
			}
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.Secret != nil && source.Secret.Name == secretName {
					return true
				}
			}
		}
	}
	return false
}

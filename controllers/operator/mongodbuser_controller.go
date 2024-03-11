package operator

import (
	"context"
	"encoding/json"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"
	"golang.org/x/xerrors"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connectionstring"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	userv1 "github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type ClusterType string

const (
	Single = "single"
	Multi  = "multi"
)

type MongoDBUserReconciler struct {
	*ReconcileCommonController
	omConnectionFactory           om.ConnectionFactory
	memberClusterClientsMap       map[string]kubernetesClient.Client
	memberClusterSecretClientsMap map[string]secrets.SecretClient
}

func newMongoDBUserReconciler(mgr manager.Manager, omFunc om.ConnectionFactory, memberClustersMap map[string]cluster.Cluster) *MongoDBUserReconciler {
	clientsMap := make(map[string]kubernetesClient.Client)
	secretClientsMap := make(map[string]secrets.SecretClient)

	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v.GetClient())
		secretClientsMap[k] = secrets.SecretClient{
			VaultClient: nil,
			KubeClient:  clientsMap[k],
		}
	}
	return &MongoDBUserReconciler{
		ReconcileCommonController:     newReconcileCommonController(mgr),
		omConnectionFactory:           omFunc,
		memberClusterClientsMap:       clientsMap,
		memberClusterSecretClientsMap: secretClientsMap,
	}
}

func (r *MongoDBUserReconciler) getUser(request reconcile.Request, log *zap.SugaredLogger) (*userv1.MongoDBUser, error) {
	user := &userv1.MongoDBUser{}
	if _, err := r.getResource(request, user, log); err != nil {
		return nil, err
	}

	// if database isn't specified default to the admin database, the recommended
	// place for creating non-$external users
	if user.Spec.Database == "" {
		user.Spec.Database = "admin"
	}

	return user, nil
}

// Use MongoDBResourceRef namespace if specified, otherwise default to user's namespace.
func getMongoDBObjectKey(user userv1.MongoDBUser) client.ObjectKey {
	mongoDBResourceNamespace := user.Namespace
	if user.Spec.MongoDBResourceRef.Namespace != "" {
		mongoDBResourceNamespace = user.Spec.MongoDBResourceRef.Namespace
	}
	return kube.ObjectKey(mongoDBResourceNamespace, user.Spec.MongoDBResourceRef.Name)
}

// getMongoDB return a MongoDB deployment of type Single or Multi cluster based on the clusterType passed
func (r *MongoDBUserReconciler) getMongoDB(user userv1.MongoDBUser) (project.Reader, error) {
	name := getMongoDBObjectKey(user)

	// Try the single cluster resource
	mdb := &mdbv1.MongoDB{}
	if err := r.client.Get(context.TODO(), name, mdb); err == nil {
		return mdb, nil
	}

	// Try the multi-cluster next
	mdbm := &mdbmulti.MongoDBMultiCluster{}
	err := r.client.Get(context.TODO(), name, mdbm)
	return mdbm, err
}

// getMongoDBConnectionBuilder returns an object that can construct a MongoDB Connection String on itself.
func (r *MongoDBUserReconciler) getMongoDBConnectionBuilder(user userv1.MongoDBUser) (connectionstring.ConnectionStringBuilder, error) {
	name := getMongoDBObjectKey(user)

	// Try single cluster resource
	mdb := &mdbv1.MongoDB{}
	if err := r.client.Get(context.TODO(), name, mdb); err == nil {
		return mdb, nil
	}

	// Try the multi-cluster next
	mdbm := &mdbmulti.MongoDBMultiCluster{}
	err := r.client.Get(context.TODO(), name, mdbm)
	return mdbm, err
}

// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbusers,mongodbusers/status,mongodbusers/finalizers},verbs=*,namespace=placeholder

// Reconciles a mongodbusers.mongodb.com Custom resource.
func (r *MongoDBUserReconciler) Reconcile(_ context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MongoDBUser", request.NamespacedName)
	log.Info("-> MongoDBUser.Reconcile")

	user, err := r.getUser(request, log)
	if err != nil {
		log.Warnf("error getting user %s", err)
		return reconcile.Result{RequeueAfter: time.Second * util.RetryTimeSec}, nil
	}

	log.Infow("MongoDBUser.Spec", "spec", user.Spec)
	var mdb project.Reader

	if user.Spec.MongoDBResourceRef.Name != "" {
		if mdb, err = r.getMongoDB(*user); err != nil {
			log.Warnf("Couldn't fetch MongoDB Single/Multi Cluster Resource with name: %s, namespace: %s, err: %s",
				user.Spec.MongoDBResourceRef.Name, user.Spec.MongoDBResourceRef.Namespace, err)
			return r.updateStatus(user, workflow.Pending(err.Error()), log)
		}
	} else {
		log.Warn("MongoDB reference not specified. Using deprecated project field.")
	}

	// this can happen when a user has registered a configmap as watched resource
	// but the user gets deleted. Reconciliation happens to this user even though it is deleted.
	// TODO: unregister config map upon MongoDBUser deletion
	if user.Namespace == "" && user.Name == "" {
		// stop reconciliation
		return workflow.Invalid("User or namespace is empty or nil").ReconcileResult()
	}

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, r.SecretClient, mdb, log)
	if err != nil {
		return r.updateStatus(user, workflow.Failed(err), log)
	}

	conn, err := connection.PrepareOpsManagerConnection(r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, user.Namespace, log)
	if err != nil {
		return r.updateStatus(user, workflow.Failed(xerrors.Errorf("Failed to prepare Ops Manager connection: %w", err)), log)
	}

	if err = r.updateConnectionStringSecret(*user, log); err != nil {
		return r.updateStatus(user, workflow.Failed(err), log)
	}

	if user.Spec.Database == authentication.ExternalDB {
		return r.handleExternalAuthUser(user, conn, log)
	} else {
		return r.handleScramShaUser(user, conn, log)
	}
}

func (r *MongoDBUserReconciler) delete(obj interface{}, log *zap.SugaredLogger) error {
	user := obj.(*userv1.MongoDBUser)

	mdb, err := r.getMongoDB(*user)
	if err != nil {
		return err
	}

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, r.SecretClient, mdb, log)
	if err != nil {
		return err
	}

	conn, err := connection.PrepareOpsManagerConnection(r.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, user.Namespace, log)
	if err != nil {
		log.Errorf("Failed to prepare Ops Manager connection: %s", err)
		return err
	}

	r.RemoveAllDependentWatchedResources(user.Namespace, kube.ObjectKeyFromApiObject(user))

	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.EnsureUserRemoved(user.Spec.Username, user.Spec.Database)
		return nil
	}, log)
}

func (r *MongoDBUserReconciler) updateConnectionStringSecret(user userv1.MongoDBUser, log *zap.SugaredLogger) error {
	var err error
	var password string

	if user.Spec.Database != authentication.ExternalDB {
		password, err = user.GetPassword(r.SecretClient)
		if err != nil {
			log.Debug("User does not have a configured password.")
		}
	}

	connectionBuilder, err := r.getMongoDBConnectionBuilder(user)
	if err != nil {
		return err
	}

	secretName := user.GetConnectionStringSecretName()
	existingSecret, err := r.client.GetSecret(types.NamespacedName{Name: secretName, Namespace: user.Namespace})
	if err != nil && !apiErrors.IsNotFound(err) {
		return err
	}
	if err == nil && !secret.HasOwnerReferences(existingSecret, user.GetOwnerReferences()) {
		return xerrors.Errorf("connection string secret %s already exists and is not managed by the operator", secretName)
	}

	mongoAuthUserURI := connectionBuilder.BuildConnectionString(user.Spec.Username, password, connectionstring.SchemeMongoDB, map[string]string{})
	mongoAuthUserSRVURI := connectionBuilder.BuildConnectionString(user.Spec.Username, password, connectionstring.SchemeMongoDBSRV, map[string]string{})

	connectionStringSecret := secret.Builder().
		SetName(secretName).
		SetNamespace(user.Namespace).
		SetField("connectionString.standard", mongoAuthUserURI).
		SetField("connectionString.standardSrv", mongoAuthUserSRVURI).
		SetField("username", user.Spec.Username).
		SetField("password", password).
		SetOwnerReferences(user.GetOwnerReferences()).
		Build()

	for _, c := range r.memberClusterSecretClientsMap {
		err = secret.CreateOrUpdate(c, connectionStringSecret)
		if err != nil {
			return err
		}
	}
	return secret.CreateOrUpdate(r.SecretClient, connectionStringSecret)
}

func AddMongoDBUserController(mgr manager.Manager, memberClustersMap map[string]cluster.Cluster) error {
	reconciler := newMongoDBUserReconciler(mgr, om.NewOpsManagerConnection, memberClustersMap)
	c, err := controller.New(util.MongoDbUserController, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}},
		&watch.ResourcesHandler{ResourceType: watch.ConfigMap, TrackedResources: reconciler.WatchedResources})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		&watch.ResourcesHandler{ResourceType: watch.Secret, TrackedResources: reconciler.WatchedResources})
	if err != nil {
		return err
	}

	// watch for changes to MongoDBUser resources
	eventHandler := MongoDBUserEventHandler{reconciler: reconciler}
	err = c.Watch(&source.Kind{Type: &userv1.MongoDBUser{}}, &eventHandler, watch.PredicatesForUser())
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbUserController)
	return nil
}

// toOmUser converts a MongoDBUser specification and optional password into an
// automation config MongoDB user. If the user has no password then a blank
// password should be provided.
func toOmUser(spec userv1.MongoDBUserSpec, password string) (om.MongoDBUser, error) {
	user := om.MongoDBUser{
		Database:                   spec.Database,
		Username:                   spec.Username,
		Roles:                      []*om.Role{},
		AuthenticationRestrictions: []string{},
		Mechanisms:                 []string{},
	}

	// only specify password if we're dealing with non-x509 users
	if spec.Database != authentication.ExternalDB {
		if err := authentication.ConfigureScramCredentials(&user, password); err != nil {
			return om.MongoDBUser{}, xerrors.Errorf("error generating SCRAM credentials: %w", err)
		}
	}

	for _, r := range spec.Roles {
		user.AddRole(&om.Role{Role: r.RoleName, Database: r.Database})
	}
	return user, nil
}

func (r *MongoDBUserReconciler) handleScramShaUser(user *userv1.MongoDBUser, conn om.Connection, log *zap.SugaredLogger) (res reconcile.Result, e error) {
	// watch the password secret in order to trigger reconciliation if the
	// password is updated
	if user.Spec.PasswordSecretKeyRef.Name != "" {
		r.AddWatchedResourceIfNotAdded(
			user.Spec.PasswordSecretKeyRef.Name,
			user.Namespace,
			watch.Secret,
			kube.ObjectKeyFromApiObject(user),
		)
	}

	shouldRetry := false
	err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if ac.Auth.Disabled ||
			(!stringutil.ContainsAny(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigScramSha256Option, util.AutomationConfigScramSha1Option)) {
			shouldRetry = true
			return xerrors.Errorf("scram Sha has not yet been configured")
		}

		password, err := user.GetPassword(r.SecretClient)
		if err != nil {
			return err
		}

		auth := ac.Auth
		if user.ChangedIdentifier() { // we've changed username or database, we need to remove the old user before adding new
			auth.RemoveUser(user.Status.Username, user.Status.Database)
		}

		desiredUser, err := toOmUser(user.Spec, password)
		if err != nil {
			return err
		}

		auth.EnsureUser(desiredUser)
		return nil
	}, log)

	if err != nil {
		if shouldRetry {
			return r.updateStatus(user, workflow.Pending(err.Error()).WithRetry(10), log)
		}
		return r.updateStatus(user, workflow.Failed(xerrors.Errorf("error updating user %w", err)), log)
	}

	annotationsToAdd, err := getAnnotationsForUserResource(user)
	if err != nil {
		return r.updateStatus(user, workflow.Failed(err), log)
	}

	if err := annotations.SetAnnotations(user, annotationsToAdd, r.client); err != nil {
		return r.updateStatus(user, workflow.Failed(err), log)
	}

	log.Infof("Finished reconciliation for MongoDBUser!")
	return r.updateStatus(user, workflow.OK(), log)
}

func (r *MongoDBUserReconciler) handleExternalAuthUser(user *userv1.MongoDBUser, conn om.Connection, log *zap.SugaredLogger) (reconcile.Result, error) {
	desiredUser, err := toOmUser(user.Spec, "")
	if err != nil {
		return r.updateStatus(user, workflow.Failed(xerrors.Errorf("error updating user %w", err)), log)
	}

	shouldRetry := false
	updateFunction := func(ac *om.AutomationConfig) error {
		if !externalAuthMechanismsAvailable(ac.Auth.DeploymentAuthMechanisms) {
			shouldRetry = true
			return xerrors.Errorf("no external authentication mechanisms (LDAP or x509) have been configured")
		}

		auth := ac.Auth
		if user.ChangedIdentifier() {
			auth.RemoveUser(user.Status.Username, user.Status.Database)
		}

		auth.EnsureUser(desiredUser)
		return nil
	}

	err = conn.ReadUpdateAutomationConfig(updateFunction, log)
	if err != nil {
		if shouldRetry {
			return r.updateStatus(user, workflow.Pending(err.Error()).WithRetry(10), log)
		}
		return r.updateStatus(user, workflow.Failed(xerrors.Errorf("error updating user %w", err)), log)
	}

	annotationsToAdd, err := getAnnotationsForUserResource(user)
	if err != nil {
		return r.updateStatus(user, workflow.Failed(err), log)
	}

	if err := annotations.SetAnnotations(user, annotationsToAdd, r.client); err != nil {
		return r.updateStatus(user, workflow.Failed(err), log)
	}

	log.Infow("Finished reconciliation for MongoDBUser!")
	return r.updateStatus(user, workflow.OK(), log)
}

func externalAuthMechanismsAvailable(mechanisms []string) bool {
	return stringutil.ContainsAny(mechanisms, util.AutomationConfigLDAPOption, util.AutomationConfigX509Option)
}

func getAnnotationsForUserResource(user *userv1.MongoDBUser) (map[string]string, error) {
	finalAnnotations := make(map[string]string)
	specBytes, err := json.Marshal(user.Spec)
	if err != nil {
		return nil, err
	}
	finalAnnotations[util.LastAchievedSpec] = string(specBytes)
	return finalAnnotations, nil
}

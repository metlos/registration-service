package handlers_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codeready-toolchain/registration-service/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/codeready-toolchain/registration-service/pkg/application/service"
	"github.com/codeready-toolchain/registration-service/pkg/configuration"
	rcontext "github.com/codeready-toolchain/registration-service/pkg/context"
	"github.com/codeready-toolchain/registration-service/pkg/proxy/handlers"
	"github.com/codeready-toolchain/registration-service/pkg/signup"
	"github.com/codeready-toolchain/registration-service/test/fake"
	spacetest "github.com/codeready-toolchain/toolchain-common/pkg/test/space"
	"github.com/gin-gonic/gin"
	"sigs.k8s.io/controller-runtime/pkg/client"

	toolchainv1alpha1 "github.com/codeready-toolchain/api/api/v1alpha1"
	commonproxy "github.com/codeready-toolchain/toolchain-common/pkg/proxy"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
)

func TestSpaceLister(t *testing.T) {

	fakeSignupService := fake.NewSignupService(
		newSignup("dancelover", "dance.lover", true),
		newSignup("movielover", "movie.lover", true),
		newSignup("pandalover", "panda.lover", true),
		newSignup("usernospace", "user.nospace", true),
		newSignup("foodlover", "food.lover", true),
		newSignup("animelover", "anime.lover", true),
		newSignup("carlover", "car.lover", true),
		newSignup("racinglover", "racing.lover", false),
		newSignup("parentspace", "parent.space", true),
		newSignup("childspace", "child.space", true),
		newSignup("grandchildspace", "grandchild.space", true),
	)

	// space that is not provisioned yet
	spaceNotProvisionedYet := fake.NewSpace("pandalover", "member-2", "pandalover")
	spaceNotProvisionedYet.Labels[toolchainv1alpha1.SpaceCreatorLabelKey] = ""

	// spacebinding associated with SpaceBindingRequest
	spaceBindingWithSBRonMovieLover := fake.NewSpaceBinding("foodlover-sb-from-sbr-on-movielover", "foodlover", "movielover", "maintainer")
	spaceBindingWithSBRonMovieLover.Labels[toolchainv1alpha1.SpaceBindingRequestLabelKey] = "foodlover-sbr"
	spaceBindingWithSBRonMovieLover.Labels[toolchainv1alpha1.SpaceBindingRequestNamespaceLabelKey] = "movielover-tenant"

	// spacebinding associated with SpaceBindingRequest on a dancelover,
	// which is also the parentSpace of foodlover
	spaceBindingWithSBRonDanceLover := fake.NewSpaceBinding("animelover-sb-from-sbr-on-dancelover", "animelover", "dancelover", "viewer")
	spaceBindingWithSBRonDanceLover.Labels[toolchainv1alpha1.SpaceBindingRequestLabelKey] = "animelover-sbr"
	spaceBindingWithSBRonDanceLover.Labels[toolchainv1alpha1.SpaceBindingRequestNamespaceLabelKey] = "dancelover-tenant"

	// spacebinding with SpaceBindingRequest but name is missing
	spaceBindingWithInvalidSBRName := fake.NewSpaceBinding("carlover-sb-from-sbr", "carlover", "animelover", "viewer")
	spaceBindingWithInvalidSBRName.Labels[toolchainv1alpha1.SpaceBindingRequestLabelKey] = "" // let's set the name to blank in order to trigger an error
	spaceBindingWithInvalidSBRName.Labels[toolchainv1alpha1.SpaceBindingRequestNamespaceLabelKey] = "anime-tenant"

	// spacebinding with SpaceBindingRequest but namespace is missing
	spaceBindingWithInvalidSBRNamespace := fake.NewSpaceBinding("animelover-sb-from-sbr", "animelover", "carlover", "viewer")
	spaceBindingWithInvalidSBRNamespace.Labels[toolchainv1alpha1.SpaceBindingRequestLabelKey] = "anime-sbr"
	spaceBindingWithInvalidSBRNamespace.Labels[toolchainv1alpha1.SpaceBindingRequestNamespaceLabelKey] = "" // let's set the name to blank in order to trigger an error

	fakeClient := fake.InitClient(t,
		// spaces
		fake.NewSpace("dancelover", "member-1", "dancelover"),
		fake.NewSpace("movielover", "member-1", "movielover"),
		fake.NewSpace("racinglover", "member-2", "racinglover"),
		fake.NewSpace("foodlover", "member-2", "foodlover", spacetest.WithSpecParentSpace("dancelover")),
		fake.NewSpace("animelover", "member-1", "animelover"),
		fake.NewSpace("carlover", "member-1", "carlover"),
		spaceNotProvisionedYet,
		fake.NewSpace("parentspace", "member-1", "parentspace"),
		fake.NewSpace("childspace", "member-1", "childspace", spacetest.WithSpecParentSpace("parentspace")),
		fake.NewSpace("grandchildspace", "member-1", "grandchildspace", spacetest.WithSpecParentSpace("childspace")),
		// noise space, user will have a different role here , just to make sure this is not returned anywhere in the tests
		fake.NewSpace("otherspace", "member-1", "otherspace", spacetest.WithSpecParentSpace("otherspace")),

		//spacebindings
		fake.NewSpaceBinding("dancer-sb1", "dancelover", "dancelover", "admin"),
		fake.NewSpaceBinding("dancer-sb2", "dancelover", "movielover", "other"),
		fake.NewSpaceBinding("moviegoer-sb", "movielover", "movielover", "admin"),
		fake.NewSpaceBinding("racer-sb", "racinglover", "racinglover", "admin"),
		fake.NewSpaceBinding("anime-sb", "animelover", "animelover", "admin"),
		fake.NewSpaceBinding("car-sb", "carlover", "carlover", "admin"),
		spaceBindingWithSBRonMovieLover,
		spaceBindingWithSBRonDanceLover,
		spaceBindingWithInvalidSBRName,
		spaceBindingWithInvalidSBRNamespace,
		fake.NewSpaceBinding("parent-sb1", "parentspace", "parentspace", "admin"),
		fake.NewSpaceBinding("child-sb1", "childspace", "childspace", "admin"),
		fake.NewSpaceBinding("grandchild-sb1", "grandchildspace", "grandchildspace", "admin"),
		// noise spacebinding, just to make sure this is not returned anywhere in the tests
		fake.NewSpaceBinding("parent-sb2", "parentspace", "otherspace", "contributor"),

		//nstemplatetier
		fake.NewBase1NSTemplateTier(),
	)

	t.Run("HandleSpaceListRequest", func(t *testing.T) {
		// given
		tests := map[string]struct {
			username             string
			expectedWs           []toolchainv1alpha1.Workspace
			expectedErr          string
			expectedErrCode      int
			expectedWorkspace    string
			overrideSignupFunc   func(ctx *gin.Context, userID, username string, checkUserSignupComplete bool) (*signup.Signup, error)
			overrideInformerFunc func() service.InformerService
		}{
			"dancelover lists spaces": {
				username: "dance.lover",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "dancelover", "admin", true),
					workspaceFor(t, fakeClient, "movielover", "other", false),
				},
				expectedErr: "",
			},
			"movielover lists spaces": {
				username: "movie.lover",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "movielover", "admin", true),
				},
				expectedErr: "",
			},
			"signup has no compliant username set": {
				username:        "racing.lover",
				expectedWs:      []toolchainv1alpha1.Workspace{},
				expectedErr:     "",
				expectedErrCode: 200,
			},
			"space not initialized yet": {
				username:        "panda.lover",
				expectedWs:      []toolchainv1alpha1.Workspace{},
				expectedErr:     "",
				expectedErrCode: 200,
			},
			"no spaces found": {
				username:        "user.nospace",
				expectedWs:      []toolchainv1alpha1.Workspace{},
				expectedErr:     "",
				expectedErrCode: 200,
			},
			"informer error": {
				username:        "dance.lover",
				expectedWs:      []toolchainv1alpha1.Workspace{},
				expectedErr:     "list spacebindings error",
				expectedErrCode: 500,
				overrideInformerFunc: func() service.InformerService {
					inf := fake.NewFakeInformer()
					inf.ListSpaceBindingFunc = func(reqs ...labels.Requirement) ([]toolchainv1alpha1.SpaceBinding, error) {
						return nil, fmt.Errorf("list spacebindings error")
					}
					return inf
				},
			},
			"get signup error": {
				username:        "dance.lover",
				expectedWs:      []toolchainv1alpha1.Workspace{},
				expectedErr:     "signup error",
				expectedErrCode: 500,
				overrideSignupFunc: func(ctx *gin.Context, userID, username string, checkUserSignupComplete bool) (*signup.Signup, error) {
					return nil, fmt.Errorf("signup error")
				},
			},
		}

		for k, tc := range tests {
			t.Run(k, func(t *testing.T) {
				// given
				signupProvider := fakeSignupService.GetSignupFromInformer
				if tc.overrideSignupFunc != nil {
					signupProvider = tc.overrideSignupFunc
				}

				informerFunc := fake.GetInformerService(fakeClient)
				if tc.overrideInformerFunc != nil {
					informerFunc = tc.overrideInformerFunc
				}

				proxyMetrics := metrics.NewProxyMetrics(prometheus.NewRegistry())

				s := &handlers.SpaceLister{
					GetSignupFunc:          signupProvider,
					GetInformerServiceFunc: informerFunc,
					ProxyMetrics:           proxyMetrics,
				}

				e := echo.New()
				req := httptest.NewRequest(http.MethodGet, "/", strings.NewReader(""))
				rec := httptest.NewRecorder()
				ctx := e.NewContext(req, rec)
				ctx.Set(rcontext.UsernameKey, tc.username)
				ctx.Set(rcontext.RequestReceivedTime, time.Now())

				// when
				err := handlers.HandleSpaceListRequest(s)(ctx)

				// then
				if tc.expectedErr != "" {
					// error case
					require.Equal(t, tc.expectedErrCode, rec.Code)
					require.Contains(t, rec.Body.String(), tc.expectedErr)
				} else {
					require.NoError(t, err)
					// list workspace case
					workspaceList, decodeErr := decodeResponseToWorkspaceList(rec.Body.Bytes())
					require.NoError(t, decodeErr)
					require.Equal(t, len(tc.expectedWs), len(workspaceList.Items))
					for i := range tc.expectedWs {
						assert.Equal(t, tc.expectedWs[i].Name, workspaceList.Items[i].Name)
						assert.Equal(t, tc.expectedWs[i].Status, workspaceList.Items[i].Status)
					}
				}
			})
		}
	})

	t.Run("HandleSpaceGetRequest", func(t *testing.T) {
		// given
		tests := map[string]struct {
			username             string
			expectedWs           []toolchainv1alpha1.Workspace
			expectedErr          string
			expectedErrCode      int
			expectedWorkspace    string
			overrideSignupFunc   func(ctx *gin.Context, userID, username string, checkUserSignupComplete bool) (*signup.Signup, error)
			overrideInformerFunc func() service.InformerService
		}{
			"dancelover gets dancelover space": {
				username: "dance.lover",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "dancelover", "admin", true,
						commonproxy.WithAvailableRoles([]string{
							"admin", "viewer",
						},
						),
						commonproxy.WithBindings([]toolchainv1alpha1.Binding{
							{
								MasterUserRecord: "animelover",
								Role:             "viewer",
								AvailableActions: []string{"update", "delete"},
								BindingRequest: &toolchainv1alpha1.BindingRequest{ // animelover was granted access to dancelover workspace using SpaceBindingRequest
									Name:      "animelover-sbr",
									Namespace: "dancelover-tenant",
								},
							},
							{
								MasterUserRecord: "dancelover",
								Role:             "admin",
								AvailableActions: []string(nil), // this is system generated so no actions for the user
							},
						}),
					),
				},
				expectedErr:       "",
				expectedWorkspace: "dancelover",
			},
			"dancelover gets movielover space": {
				username: "dance.lover",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "movielover", "other", false,
						commonproxy.WithAvailableRoles([]string{
							"admin", "viewer",
						}),
						commonproxy.WithBindings([]toolchainv1alpha1.Binding{
							{
								MasterUserRecord: "dancelover",
								Role:             "other",
								AvailableActions: []string(nil), // this is system generated so no actions for the user
							},
							{
								MasterUserRecord: "foodlover",
								Role:             "maintainer",
								AvailableActions: []string{"update", "delete"},
								BindingRequest: &toolchainv1alpha1.BindingRequest{ // foodlover was granted access to movielover workspace using SpaceBindingRequest
									Name:      "foodlover-sbr",
									Namespace: "movielover-tenant",
								},
							},
							{
								MasterUserRecord: "movielover",
								Role:             "admin",
								AvailableActions: []string(nil), // this is system generated so no actions for the user
							},
						}),
					),
				},
				expectedErr:       "",
				expectedWorkspace: "movielover",
			},
			"dancelover gets foodlover space": {
				username: "dance.lover",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "foodlover", "admin", false,
						commonproxy.WithAvailableRoles([]string{
							"admin", "viewer",
						}),
						commonproxy.WithBindings([]toolchainv1alpha1.Binding{
							{
								MasterUserRecord: "animelover",
								Role:             "viewer",             // animelover was granted access via SBR , but on the parentSpace,
								AvailableActions: []string{"override"}, // since the binding is inherited from parent space, then it can only be overridden
							},
							{
								MasterUserRecord: "dancelover",
								Role:             "admin",              // dancelover is admin since it's admin on the parent space,
								AvailableActions: []string{"override"}, // since the binding is inherited from parent space, then it can only be overridden
							},
						}),
					),
				},
				expectedErr:       "",
				expectedWorkspace: "foodlover",
			},
			"movielover gets movielover space": {
				username: "movie.lover",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "movielover", "admin", true,
						commonproxy.WithAvailableRoles([]string{
							"admin", "viewer",
						}),
						// bindings are in alphabetical order using the MUR name
						commonproxy.WithBindings([]toolchainv1alpha1.Binding{
							{
								MasterUserRecord: "dancelover",
								Role:             "other",
								AvailableActions: []string(nil), // this is system generated so no actions for the user
							},
							{
								MasterUserRecord: "foodlover",
								Role:             "maintainer",
								AvailableActions: []string{"update", "delete"},
								BindingRequest: &toolchainv1alpha1.BindingRequest{
									Name:      "foodlover-sbr",
									Namespace: "movielover-tenant",
								},
							},
							{
								MasterUserRecord: "movielover",
								Role:             "admin",
								AvailableActions: []string(nil), // this is system generated so no actions for the user
							},
						}),
					),
				},
				expectedErr:       "",
				expectedWorkspace: "movielover",
			},
			"movielover cannot get dancelover space": {
				username:          "movie.lover",
				expectedWs:        []toolchainv1alpha1.Workspace{},
				expectedErr:       "\"workspaces.toolchain.dev.openshift.com \\\"dancelover\\\" not found\"",
				expectedWorkspace: "dancelover",
				expectedErrCode:   404,
			},
			"signup not ready yet": {
				username: "movie.lover",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "movielover", "admin", true,
						commonproxy.WithAvailableRoles([]string{
							"admin", "viewer",
						}),
						commonproxy.WithBindings([]toolchainv1alpha1.Binding{
							{
								MasterUserRecord: "dancelover",
								Role:             "other",
								AvailableActions: []string(nil), // this is system generated so no actions for the user
							},
							{
								MasterUserRecord: "foodlover", // foodlover was granted access to movielover workspace using SpaceBindingRequest
								Role:             "maintainer",
								AvailableActions: []string{"update", "delete"},
								BindingRequest: &toolchainv1alpha1.BindingRequest{
									Name:      "foodlover-sbr",
									Namespace: "movielover-tenant",
								},
							},
							{
								MasterUserRecord: "movielover",
								Role:             "admin",
								AvailableActions: []string(nil), // this is system generated so no actions for the user
							},
						}),
					),
				},
				expectedErr:       "",
				expectedWorkspace: "movielover",
			},
			"get nstemplatetier error": {
				username:        "dance.lover",
				expectedWs:      []toolchainv1alpha1.Workspace{},
				expectedErr:     "nstemplatetier error",
				expectedErrCode: 500,
				overrideInformerFunc: func() service.InformerService {
					informerFunc := fake.GetInformerService(fakeClient, fake.WithGetNSTemplateTierFunc(func(tierName string) (*toolchainv1alpha1.NSTemplateTier, error) {
						return nil, fmt.Errorf("nstemplatetier error")
					}))
					return informerFunc()
				},
				expectedWorkspace: "dancelover",
			},
			"get signup error": {
				username:        "dance.lover",
				expectedWs:      []toolchainv1alpha1.Workspace{},
				expectedErr:     "signup error",
				expectedErrCode: 500,
				overrideSignupFunc: func(ctx *gin.Context, userID, username string, checkUserSignupComplete bool) (*signup.Signup, error) {
					return nil, fmt.Errorf("signup error")
				},
				expectedWorkspace: "dancelover",
			},
			"signup has no compliant username set": {
				username:          "racing.lover",
				expectedWs:        []toolchainv1alpha1.Workspace{},
				expectedErr:       "\"workspaces.toolchain.dev.openshift.com \\\"racinglover\\\" not found\"",
				expectedErrCode:   404,
				expectedWorkspace: "racinglover",
			},
			"list spacebindings error": {
				username:        "dance.lover",
				expectedWs:      []toolchainv1alpha1.Workspace{},
				expectedErr:     "list spacebindings error",
				expectedErrCode: 500,
				overrideInformerFunc: func() service.InformerService {
					listSpaceBindingFunc := func(reqs ...labels.Requirement) ([]toolchainv1alpha1.SpaceBinding, error) {
						return nil, fmt.Errorf("list spacebindings error")
					}
					return fake.GetInformerService(fakeClient, fake.WithListSpaceBindingFunc(listSpaceBindingFunc))()
				},
				expectedWorkspace: "dancelover",
			},
			"unable to get space": {
				username:        "dance.lover",
				expectedWs:      []toolchainv1alpha1.Workspace{},
				expectedErr:     "\"workspaces.toolchain.dev.openshift.com \\\"dancelover\\\" not found\"",
				expectedErrCode: 404,
				overrideInformerFunc: func() service.InformerService {
					getSpaceFunc := func(name string) (*toolchainv1alpha1.Space, error) {
						return nil, fmt.Errorf("no space")
					}
					return fake.GetInformerService(fakeClient, fake.WithGetSpaceFunc(getSpaceFunc))()
				},
				expectedWorkspace: "dancelover",
			},
			"unable to get parent-space": {
				username:        "food.lover",
				expectedWs:      []toolchainv1alpha1.Workspace{},
				expectedErr:     "Internal error occurred: unable to get parent-space: parent-space error",
				expectedErrCode: 500,
				overrideInformerFunc: func() service.InformerService {
					getSpaceFunc := func(name string) (*toolchainv1alpha1.Space, error) {
						if name == "dancelover" {
							// return the error only when trying to get the parent space
							return nil, fmt.Errorf("parent-space error")
						}
						// return the foodlover space
						return fake.NewSpace("foodlover", "member-2", "foodlover", spacetest.WithSpecParentSpace("dancelover")), nil
					}
					return fake.GetInformerService(fakeClient, fake.WithGetSpaceFunc(getSpaceFunc))()
				},
				expectedWorkspace: "foodlover",
			},
			"error spaceBinding request has no name": {
				username: "anime.lover",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "animelover", "admin", true,
						commonproxy.WithAvailableRoles([]string{
							"admin", "viewer",
						}),
					),
				},
				expectedErr:       "Internal error occurred: SpaceBindingRequest name not found on binding: carlover-sb-from-sbr",
				expectedErrCode:   500,
				expectedWorkspace: "animelover",
			},
			"error spaceBinding request has no namespace set": {
				username: "car.lover",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "carlover", "admin", true,
						commonproxy.WithAvailableRoles([]string{
							"admin", "viewer",
						}),
					),
				},
				expectedErr:       "Internal error occurred: SpaceBindingRequest namespace not found on binding: animelover-sb-from-sbr",
				expectedErrCode:   500,
				expectedWorkspace: "carlover",
			},
			"parent can list parentspace": {
				username: "parent.space",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "parentspace", "admin", true,
						commonproxy.WithAvailableRoles([]string{
							"admin", "viewer",
						}),
						commonproxy.WithBindings([]toolchainv1alpha1.Binding{
							{
								MasterUserRecord: "parentspace",
								Role:             "admin",
								AvailableActions: []string(nil), // this is system generated so no actions for the user
							},
						}),
					),
				},
				expectedWorkspace: "parentspace",
			},
			"parent can list childspace": {
				username: "parent.space",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "childspace", "admin", false,
						commonproxy.WithAvailableRoles([]string{
							"admin", "viewer",
						}),
						commonproxy.WithBindings([]toolchainv1alpha1.Binding{
							{
								MasterUserRecord: "childspace",
								Role:             "admin",
								AvailableActions: []string(nil),
							},
							{
								MasterUserRecord: "parentspace",
								Role:             "admin",
								AvailableActions: []string{"override"},
							},
						}),
					),
				},
				expectedWorkspace: "childspace",
			},
			"parent can list grandchildspace": {
				username: "parent.space",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "grandchildspace", "admin", false,
						commonproxy.WithAvailableRoles([]string{
							"admin", "viewer",
						}),
						commonproxy.WithBindings([]toolchainv1alpha1.Binding{
							{
								MasterUserRecord: "childspace",
								Role:             "admin",
								AvailableActions: []string{"override"},
							},
							{
								MasterUserRecord: "grandchildspace",
								Role:             "admin",
								AvailableActions: []string(nil),
							},
							{
								MasterUserRecord: "parentspace",
								Role:             "admin",
								AvailableActions: []string{"override"},
							},
						}),
					),
				},
				expectedWorkspace: "grandchildspace",
			},
			"child cannot list parentspace": {
				username:          "child.space",
				expectedWs:        []toolchainv1alpha1.Workspace{},
				expectedErr:       "\"workspaces.toolchain.dev.openshift.com \\\"parentspace\\\" not found\"",
				expectedErrCode:   404,
				expectedWorkspace: "parentspace",
			},
			"child can list childspace": {
				username:          "child.space",
				expectedWorkspace: "childspace",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "childspace", "admin", true,
						commonproxy.WithAvailableRoles([]string{
							"admin", "viewer",
						}),
						commonproxy.WithBindings([]toolchainv1alpha1.Binding{
							{
								MasterUserRecord: "childspace",
								Role:             "admin",
								AvailableActions: []string(nil),
							},
							{
								MasterUserRecord: "parentspace",
								Role:             "admin",
								AvailableActions: []string{"override"},
							},
						}),
					),
				},
			},
			"child can list grandchildspace": {
				username:          "child.space",
				expectedWorkspace: "grandchildspace",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "grandchildspace", "admin", false,
						commonproxy.WithAvailableRoles([]string{
							"admin", "viewer",
						}),
						commonproxy.WithBindings([]toolchainv1alpha1.Binding{
							{
								MasterUserRecord: "childspace",
								Role:             "admin",
								AvailableActions: []string{"override"},
							},
							{
								MasterUserRecord: "grandchildspace",
								Role:             "admin",
								AvailableActions: []string(nil),
							},
							{
								MasterUserRecord: "parentspace",
								Role:             "admin",
								AvailableActions: []string{"override"},
							},
						}),
					),
				},
			},
			"grandchild can list grandchildspace": {
				username: "grandchild.space",
				expectedWs: []toolchainv1alpha1.Workspace{
					workspaceFor(t, fakeClient, "grandchildspace", "admin", true,
						commonproxy.WithAvailableRoles([]string{
							"admin", "viewer",
						}),
						commonproxy.WithBindings([]toolchainv1alpha1.Binding{
							{
								MasterUserRecord: "childspace",
								Role:             "admin",
								AvailableActions: []string{"override"},
							},
							{
								MasterUserRecord: "grandchildspace",
								Role:             "admin",
								AvailableActions: []string(nil),
							},
							{
								MasterUserRecord: "parentspace",
								Role:             "admin",
								AvailableActions: []string{"override"},
							},
						}),
					),
				},
				expectedWorkspace: "grandchildspace",
			},
			"grandchild cannot list parentspace": {
				username:          "grandchild.space",
				expectedWorkspace: "parentspace",
				expectedErr:       "\"workspaces.toolchain.dev.openshift.com \\\"parentspace\\\" not found\"",
				expectedErrCode:   404,
				expectedWs:        []toolchainv1alpha1.Workspace{},
			},
			"grandchild cannot list childspace": {
				username:          "grandchild.space",
				expectedWorkspace: "childspace",
				expectedErr:       "\"workspaces.toolchain.dev.openshift.com \\\"childspace\\\" not found\"",
				expectedErrCode:   404,
				expectedWs:        []toolchainv1alpha1.Workspace{},
			},
		}

		for k, tc := range tests {
			t.Run(k, func(t *testing.T) {
				// given
				signupProvider := fakeSignupService.GetSignupFromInformer
				if tc.overrideSignupFunc != nil {
					signupProvider = tc.overrideSignupFunc
				}

				informerFunc := fake.GetInformerService(fakeClient)
				if tc.overrideInformerFunc != nil {
					informerFunc = tc.overrideInformerFunc
				}

				proxyMetrics := metrics.NewProxyMetrics(prometheus.NewRegistry())

				s := &handlers.SpaceLister{
					GetSignupFunc:          signupProvider,
					GetInformerServiceFunc: informerFunc,
					ProxyMetrics:           proxyMetrics,
				}

				e := echo.New()
				req := httptest.NewRequest(http.MethodGet, "/", strings.NewReader(""))
				rec := httptest.NewRecorder()
				ctx := e.NewContext(req, rec)
				ctx.Set(rcontext.UsernameKey, tc.username)
				ctx.Set(rcontext.RequestReceivedTime, time.Now())
				ctx.SetParamNames("workspace")
				ctx.SetParamValues(tc.expectedWorkspace)

				// when
				err := handlers.HandleSpaceGetRequest(s)(ctx)

				// then
				if tc.expectedErr != "" {
					// error case
					require.Equal(t, tc.expectedErrCode, rec.Code)
					require.Contains(t, rec.Body.String(), tc.expectedErr)
				} else {
					require.NoError(t, err)
					// get workspace case
					workspace, decodeErr := decodeResponseToWorkspace(rec.Body.Bytes())
					require.NoError(t, decodeErr)
					require.Equal(t, 1, len(tc.expectedWs), "test case should have exactly one expected item since it's a get request")
					for i := range tc.expectedWs {
						assert.Equal(t, tc.expectedWs[i].Name, workspace.Name)
						assert.Equal(t, tc.expectedWs[i].Status, workspace.Status)
					}
				}
			})
		}
	})
}

func newSignup(signupName, username string, ready bool) fake.SignupDef {
	compliantUsername := signupName
	if !ready {
		// signup is not ready, let's set compliant username to blank
		compliantUsername = ""
	}
	us := fake.Signup(signupName, &signup.Signup{
		Name:              signupName,
		Username:          username,
		CompliantUsername: compliantUsername,
		Status: signup.Status{
			Ready: ready,
		},
	})

	return us
}

func decodeResponseToWorkspace(data []byte) (*toolchainv1alpha1.Workspace, error) {
	decoder := serializer.NewCodecFactory(scheme.Scheme).UniversalDecoder()
	obj := &toolchainv1alpha1.Workspace{}
	err := runtime.DecodeInto(decoder, data, obj)
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func decodeResponseToWorkspaceList(data []byte) (*toolchainv1alpha1.WorkspaceList, error) {
	decoder := serializer.NewCodecFactory(scheme.Scheme).UniversalDecoder()
	obj := &toolchainv1alpha1.WorkspaceList{}
	err := runtime.DecodeInto(decoder, data, obj)
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func workspaceFor(t *testing.T, fakeClient client.Client, name, role string, isHomeWorkspace bool, additionalWSOptions ...commonproxy.WorkspaceOption) toolchainv1alpha1.Workspace {
	// get the space for the user
	space := &toolchainv1alpha1.Space{}
	err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: name, Namespace: configuration.Namespace()}, space)
	require.NoError(t, err)

	// create the workspace based on the space
	commonWSoptions := []commonproxy.WorkspaceOption{
		commonproxy.WithObjectMetaFrom(space.ObjectMeta),
		commonproxy.WithNamespaces([]toolchainv1alpha1.SpaceNamespace{
			{
				Name: "john-dev",
				Type: "default",
			},
			{
				Name: "john-stage",
			},
		}),
		commonproxy.WithOwner(name),
		commonproxy.WithRole(role),
	}
	ws := commonproxy.NewWorkspace(name,
		append(commonWSoptions, additionalWSOptions...)...,
	)
	// if the user is the same as the one who created the workspace, then expect type should be "home"
	if isHomeWorkspace {
		ws.Status.Type = "home"
	}
	return *ws
}

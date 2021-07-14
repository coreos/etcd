// Copyright 2016 The etcd Authors
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

package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/stretchr/testify/assert"
	"strings"
	"sync"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/server/v3/storage/backend"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/metadata"
)

func dummyIndexWaiter(index uint64) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		ch <- struct{}{}
	}()
	return ch
}

// TestNewAuthStoreRevision ensures newly auth store
// keeps the old revision when there are no changes.
func TestNewAuthStoreRevision(t *testing.T) {
	b, tPath := betesting.NewDefaultTmpBackend(t)

	tp, err := NewTokenProvider(zap.NewExample(), tokenTypeSimple, dummyIndexWaiter, simpleTokenTTLDefault)
	if err != nil {
		t.Fatal(err)
	}
	as := NewAuthStore(zap.NewExample(), b, tp, bcrypt.MinCost)
	err = enableAuthAndCreateRoot(as)
	if err != nil {
		t.Fatal(err)
	}
	old := as.Revision()
	as.Close()
	b.Close()

	// no changes to commit
	b2 := backend.NewDefaultBackend(tPath)
	defer b2.Close()
	as = NewAuthStore(zap.NewExample(), b2, tp, bcrypt.MinCost)
	defer as.Close()
	new := as.Revision()

	if old != new {
		t.Fatalf("expected revision %d, got %d", old, new)
	}
}

// TestNewAuthStoreBryptCost ensures that NewAuthStore uses default when given bcrypt-cost is invalid
func TestNewAuthStoreBcryptCost(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, b)

	tp, err := NewTokenProvider(zap.NewExample(), tokenTypeSimple, dummyIndexWaiter, simpleTokenTTLDefault)
	if err != nil {
		t.Fatal(err)
	}

	invalidCosts := [2]int{bcrypt.MinCost - 1, bcrypt.MaxCost + 1}
	for _, invalidCost := range invalidCosts {
		as := NewAuthStore(zap.NewExample(), b, tp, invalidCost)
		defer as.Close()
		if as.BcryptCost() != bcrypt.DefaultCost {
			t.Fatalf("expected DefaultCost when bcryptcost is invalid")
		}
	}
}

func encodePassword(s string) string {
	hashedPassword, _ := bcrypt.GenerateFromPassword([]byte(s), bcrypt.MinCost)
	return base64.StdEncoding.EncodeToString([]byte(hashedPassword))
}

func setupAuthStore(t *testing.T) (store *authStore, teardownfunc func(t *testing.T)) {
	b, _ := betesting.NewDefaultTmpBackend(t)

	tp, err := NewTokenProvider(zap.NewExample(), tokenTypeSimple, dummyIndexWaiter, simpleTokenTTLDefault)
	if err != nil {
		t.Fatal(err)
	}
	as := NewAuthStore(zap.NewExample(), b, tp, bcrypt.MinCost)
	err = enableAuthAndCreateRoot(as)
	if err != nil {
		t.Fatal(err)
	}

	// adds a new role
	_, err = as.RoleAdd(&pb.AuthRoleAddRequest{Name: "role-test"})
	if err != nil {
		t.Fatal(err)
	}

	ua := &pb.AuthUserAddRequest{Name: "foo", HashedPassword: encodePassword("bar"), Options: &authpb.UserAddOptions{NoPassword: false}}
	_, err = as.UserAdd(ua) // add a non-existing user
	if err != nil {
		t.Fatal(err)
	}

	tearDown := func(_ *testing.T) {
		b.Close()
		as.Close()
	}
	return as, tearDown
}

func enableAuthAndCreateRoot(as *authStore) error {
	_, err := as.UserAdd(&pb.AuthUserAddRequest{Name: "root", HashedPassword: encodePassword("root"), Options: &authpb.UserAddOptions{NoPassword: false}})
	if err != nil {
		return err
	}

	_, err = as.RoleAdd(&pb.AuthRoleAddRequest{Name: "root"})
	if err != nil {
		return err
	}

	_, err = as.UserGrantRole(&pb.AuthUserGrantRoleRequest{User: "root", Role: "root"})
	if err != nil {
		return err
	}

	return as.AuthEnable()
}

func TestUserAdd(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	ua := &pb.AuthUserAddRequest{Name: "foo", Options: &authpb.UserAddOptions{NoPassword: false}}
	_, err := as.UserAdd(ua) // add an existing user
	if err == nil {
		t.Fatalf("expected %v, got %v", ErrUserAlreadyExist, err)
	}
	if err != ErrUserAlreadyExist {
		t.Fatalf("expected %v, got %v", ErrUserAlreadyExist, err)
	}

	ua = &pb.AuthUserAddRequest{Name: "", Options: &authpb.UserAddOptions{NoPassword: false}}
	_, err = as.UserAdd(ua) // add a user with empty name
	if err != ErrUserEmpty {
		t.Fatal(err)
	}
}

func TestRecover(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer as.Close()
	defer tearDown(t)

	as.enabled = false
	as.Recover(as.be)

	if !as.IsAuthEnabled() {
		t.Fatalf("expected auth enabled got disabled")
	}
}

func TestCheckPassword(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	// auth a non-existing user
	_, err := as.CheckPassword("foo-test", "bar")
	if err == nil {
		t.Fatalf("expected %v, got %v", ErrAuthFailed, err)
	}
	if err != ErrAuthFailed {
		t.Fatalf("expected %v, got %v", ErrAuthFailed, err)
	}

	// auth an existing user with correct password
	_, err = as.CheckPassword("foo", "bar")
	if err != nil {
		t.Fatal(err)
	}

	// auth an existing user but with wrong password
	_, err = as.CheckPassword("foo", "")
	if err == nil {
		t.Fatalf("expected %v, got %v", ErrAuthFailed, err)
	}
	if err != ErrAuthFailed {
		t.Fatalf("expected %v, got %v", ErrAuthFailed, err)
	}
}

func TestUserDelete(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	// delete an existing user
	ud := &pb.AuthUserDeleteRequest{Name: "foo"}
	_, err := as.UserDelete(ud)
	if err != nil {
		t.Fatal(err)
	}

	// delete a non-existing user
	_, err = as.UserDelete(ud)
	if err == nil {
		t.Fatalf("expected %v, got %v", ErrUserNotFound, err)
	}
	if err != ErrUserNotFound {
		t.Fatalf("expected %v, got %v", ErrUserNotFound, err)
	}
}

func TestUserChangePassword(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	ctx1 := context.WithValue(context.WithValue(context.TODO(), AuthenticateParamIndex{}, uint64(1)), AuthenticateParamSimpleTokenPrefix{}, "dummy")
	_, err := as.Authenticate(ctx1, "foo", "bar")
	if err != nil {
		t.Fatal(err)
	}

	_, err = as.UserChangePassword(&pb.AuthUserChangePasswordRequest{Name: "foo", HashedPassword: encodePassword("baz")})
	if err != nil {
		t.Fatal(err)
	}

	ctx2 := context.WithValue(context.WithValue(context.TODO(), AuthenticateParamIndex{}, uint64(2)), AuthenticateParamSimpleTokenPrefix{}, "dummy")
	_, err = as.Authenticate(ctx2, "foo", "baz")
	if err != nil {
		t.Fatal(err)
	}

	// change a non-existing user
	_, err = as.UserChangePassword(&pb.AuthUserChangePasswordRequest{Name: "foo-test", HashedPassword: encodePassword("bar")})
	if err == nil {
		t.Fatalf("expected %v, got %v", ErrUserNotFound, err)
	}
	if err != ErrUserNotFound {
		t.Fatalf("expected %v, got %v", ErrUserNotFound, err)
	}
}

func TestRoleAdd(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	// adds a new role
	_, err := as.RoleAdd(&pb.AuthRoleAddRequest{Name: "role-test-1"})
	if err != nil {
		t.Fatal(err)
	}

	// add a role with empty name
	_, err = as.RoleAdd(&pb.AuthRoleAddRequest{Name: ""})
	if err != ErrRoleEmpty {
		t.Fatal(err)
	}
}

func TestUserGrant(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	// grants a role to the user
	_, err := as.UserGrantRole(&pb.AuthUserGrantRoleRequest{User: "foo", Role: "role-test"})
	if err != nil {
		t.Fatal(err)
	}

	// grants a role to a non-existing user
	_, err = as.UserGrantRole(&pb.AuthUserGrantRoleRequest{User: "foo-test", Role: "role-test"})
	if err == nil {
		t.Errorf("expected %v, got %v", ErrUserNotFound, err)
	}
	if err != ErrUserNotFound {
		t.Errorf("expected %v, got %v", ErrUserNotFound, err)
	}
}

func TestHasRole(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	// grants a role to the user
	_, err := as.UserGrantRole(&pb.AuthUserGrantRoleRequest{User: "foo", Role: "role-test"})
	if err != nil {
		t.Fatal(err)
	}

	// checks role reflects correctly
	hr := as.HasRole("foo", "role-test")
	if !hr {
		t.Fatal("expected role granted, got false")
	}

	// checks non existent role
	hr = as.HasRole("foo", "non-existent-role")
	if hr {
		t.Fatal("expected role not found, got true")
	}

	// checks non existent user
	hr = as.HasRole("nouser", "role-test")
	if hr {
		t.Fatal("expected user not found got true")
	}
}

func TestIsOpPermitted(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	// add new role
	_, err := as.RoleAdd(&pb.AuthRoleAddRequest{Name: "role-test-1"})
	if err != nil {
		t.Fatal(err)
	}

	perm := &authpb.Permission{
		PermType: authpb.WRITE,
		Key:      []byte("Keys"),
		RangeEnd: []byte("RangeEnd"),
	}

	_, err = as.RoleGrantPermission(&pb.AuthRoleGrantPermissionRequest{
		Name: "role-test-1",
		Perm: perm,
	})
	if err != nil {
		t.Fatal(err)
	}

	// grants a role to the user
	_, err = as.UserGrantRole(&pb.AuthUserGrantRoleRequest{User: "foo", Role: "role-test-1"})
	if err != nil {
		t.Fatal(err)
	}

	// check permission reflected to user

	err = as.isOpPermitted("foo", as.Revision(), perm.Key, perm.RangeEnd, perm.PermType)
	if err != nil {
		t.Fatal(err)
	}
}

func TestGetUser(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	_, err := as.UserGrantRole(&pb.AuthUserGrantRoleRequest{User: "foo", Role: "role-test"})
	if err != nil {
		t.Fatal(err)
	}

	u, err := as.UserGet(&pb.AuthUserGetRequest{Name: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expect user not nil, got nil")
	}
	expected := []string{"role-test"}

	assert.Equal(t, expected, u.Roles)

	// check non existent user
	_, err = as.UserGet(&pb.AuthUserGetRequest{Name: "nouser"})
	if err == nil {
		t.Errorf("expected %v, got %v", ErrUserNotFound, err)
	}
}

func TestListUsers(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	ua := &pb.AuthUserAddRequest{Name: "user1", HashedPassword: encodePassword("pwd1"), Options: &authpb.UserAddOptions{NoPassword: false}}
	_, err := as.UserAdd(ua) // add a non-existing user
	if err != nil {
		t.Fatal(err)
	}

	ul, err := as.UserList(&pb.AuthUserListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(ul.Users, "root") {
		t.Errorf("expected %v in %v", "root", ul.Users)
	}
	if !contains(ul.Users, "user1") {
		t.Errorf("expected %v in %v", "user1", ul.Users)
	}
}

func TestRoleGrantPermission(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	_, err := as.RoleAdd(&pb.AuthRoleAddRequest{Name: "role-test-1"})
	if err != nil {
		t.Fatal(err)
	}

	perm := &authpb.Permission{
		PermType: authpb.WRITE,
		Key:      []byte("Keys"),
		RangeEnd: []byte("RangeEnd"),
	}
	_, err = as.RoleGrantPermission(&pb.AuthRoleGrantPermissionRequest{
		Name: "role-test-1",
		Perm: perm,
	})

	if err != nil {
		t.Error(err)
	}

	r, err := as.RoleGet(&pb.AuthRoleGetRequest{Role: "role-test-1"})
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, perm, r.Perm[0])

	// trying to grant nil permissions returns an error (and doesn't change the actual permissions!)
	_, err = as.RoleGrantPermission(&pb.AuthRoleGrantPermissionRequest{
		Name: "role-test-1",
	})

	if err != ErrPermissionNotGiven {
		t.Error(err)
	}

	r, err = as.RoleGet(&pb.AuthRoleGetRequest{Role: "role-test-1"})
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, perm, r.Perm[0])
}

func TestRootRoleGrantPermission(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	perm := &authpb.Permission{
		PermType: authpb.WRITE,
		Key:      []byte("Keys"),
		RangeEnd: []byte("RangeEnd"),
	}
	_, err := as.RoleGrantPermission(&pb.AuthRoleGrantPermissionRequest{
		Name: "root",
		Perm: perm,
	})

	if err != nil {
		t.Error(err)
	}

	r, err := as.RoleGet(&pb.AuthRoleGetRequest{Role: "root"})
	if err != nil {
		t.Fatal(err)
	}

	//whatever grant permission to root, it always return root permission.
	expectPerm := &authpb.Permission{
		PermType: authpb.READWRITE,
		Key:      []byte{},
		RangeEnd: []byte{0},
	}

	assert.Equal(t, expectPerm, r.Perm[0])
}

func TestRoleRevokePermission(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	_, err := as.RoleAdd(&pb.AuthRoleAddRequest{Name: "role-test-1"})
	if err != nil {
		t.Fatal(err)
	}

	perm := &authpb.Permission{
		PermType: authpb.WRITE,
		Key:      []byte("Keys"),
		RangeEnd: []byte("RangeEnd"),
	}
	_, err = as.RoleGrantPermission(&pb.AuthRoleGrantPermissionRequest{
		Name: "role-test-1",
		Perm: perm,
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = as.RoleGet(&pb.AuthRoleGetRequest{Role: "role-test-1"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = as.RoleRevokePermission(&pb.AuthRoleRevokePermissionRequest{
		Role:     "role-test-1",
		Key:      []byte("Keys"),
		RangeEnd: []byte("RangeEnd"),
	})
	if err != nil {
		t.Fatal(err)
	}

	var r *pb.AuthRoleGetResponse
	r, err = as.RoleGet(&pb.AuthRoleGetRequest{Role: "role-test-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Perm) != 0 {
		t.Errorf("expected %v, got %v", 0, len(r.Perm))
	}
}

func TestUserRevokePermission(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	_, err := as.RoleAdd(&pb.AuthRoleAddRequest{Name: "role-test-1"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = as.UserGrantRole(&pb.AuthUserGrantRoleRequest{User: "foo", Role: "role-test"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = as.UserGrantRole(&pb.AuthUserGrantRoleRequest{User: "foo", Role: "role-test-1"})
	if err != nil {
		t.Fatal(err)
	}

	u, err := as.UserGet(&pb.AuthUserGetRequest{Name: "foo"})
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{"role-test", "role-test-1"}

	assert.Equal(t, expected, u.Roles)

	_, err = as.UserRevokeRole(&pb.AuthUserRevokeRoleRequest{Name: "foo", Role: "role-test-1"})
	if err != nil {
		t.Fatal(err)
	}

	u, err = as.UserGet(&pb.AuthUserGetRequest{Name: "foo"})
	if err != nil {
		t.Fatal(err)
	}

	expected = []string{"role-test"}

	assert.Equal(t, expected, u.Roles)
}

func TestRoleDelete(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	_, err := as.RoleDelete(&pb.AuthRoleDeleteRequest{Role: "role-test"})
	if err != nil {
		t.Fatal(err)
	}
	rl, err := as.RoleList(&pb.AuthRoleListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"root"}

	assert.Equal(t, expected, rl.Roles)
}

func TestAuthInfoFromCtx(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	ctx := context.Background()
	ai, err := as.AuthInfoFromCtx(ctx)
	if err != nil && ai != nil {
		t.Errorf("expected (nil, nil), got (%v, %v)", ai, err)
	}

	// as if it came from RPC
	ctx = metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{"tokens": "dummy"}))
	ai, err = as.AuthInfoFromCtx(ctx)
	if err != nil && ai != nil {
		t.Errorf("expected (nil, nil), got (%v, %v)", ai, err)
	}

	ctx = context.WithValue(context.WithValue(context.TODO(), AuthenticateParamIndex{}, uint64(1)), AuthenticateParamSimpleTokenPrefix{}, "dummy")
	resp, err := as.Authenticate(ctx, "foo", "bar")
	if err != nil {
		t.Error(err)
	}

	ctx = metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{rpctypes.TokenFieldNameGRPC: "Invalid Token"}))
	_, err = as.AuthInfoFromCtx(ctx)
	if err != ErrInvalidAuthToken {
		t.Errorf("expected %v, got %v", ErrInvalidAuthToken, err)
	}

	ctx = metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{rpctypes.TokenFieldNameGRPC: "Invalid.Token"}))
	_, err = as.AuthInfoFromCtx(ctx)
	if err != ErrInvalidAuthToken {
		t.Errorf("expected %v, got %v", ErrInvalidAuthToken, err)
	}

	ctx = metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{rpctypes.TokenFieldNameGRPC: resp.Token}))
	ai, err = as.AuthInfoFromCtx(ctx)
	if err != nil {
		t.Error(err)
	}
	if ai.Username != "foo" {
		t.Errorf("expected %v, got %v", "foo", ai.Username)
	}
}

func TestAuthDisable(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	as.AuthDisable()
	ctx := context.WithValue(context.WithValue(context.TODO(), AuthenticateParamIndex{}, uint64(2)), AuthenticateParamSimpleTokenPrefix{}, "dummy")
	_, err := as.Authenticate(ctx, "foo", "bar")
	if err != ErrAuthNotEnabled {
		t.Errorf("expected %v, got %v", ErrAuthNotEnabled, err)
	}

	// Disabling disabled auth to make sure it can return safely if store is already disabled.
	as.AuthDisable()
	_, err = as.Authenticate(ctx, "foo", "bar")
	if err != ErrAuthNotEnabled {
		t.Errorf("expected %v, got %v", ErrAuthNotEnabled, err)
	}
}

func TestIsAuthEnabled(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	// enable authentication to test the first possible condition
	as.AuthEnable()

	status := as.IsAuthEnabled()
	ctx := context.WithValue(context.WithValue(context.TODO(), AuthenticateParamIndex{}, uint64(2)), AuthenticateParamSimpleTokenPrefix{}, "dummy")
	_, _ = as.Authenticate(ctx, "foo", "bar")
	if status != true {
		t.Errorf("expected %v, got %v", true, false)
	}

	// Disabling disabled auth to test the other condition that can be return
	as.AuthDisable()

	status = as.IsAuthEnabled()
	_, _ = as.Authenticate(ctx, "foo", "bar")
	if status != false {
		t.Errorf("expected %v, got %v", false, true)
	}
}

// TestAuthRevisionRace ensures that access to authStore.revision is thread-safe.
func TestAuthInfoFromCtxRace(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, b)

	tp, err := NewTokenProvider(zap.NewExample(), tokenTypeSimple, dummyIndexWaiter, simpleTokenTTLDefault)
	if err != nil {
		t.Fatal(err)
	}
	as := NewAuthStore(zap.NewExample(), b, tp, bcrypt.MinCost)
	defer as.Close()

	donec := make(chan struct{})
	go func() {
		defer close(donec)
		ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{rpctypes.TokenFieldNameGRPC: "test"}))
		as.AuthInfoFromCtx(ctx)
	}()
	as.UserAdd(&pb.AuthUserAddRequest{Name: "test", Options: &authpb.UserAddOptions{NoPassword: false}})
	<-donec
}

func TestIsAdminPermitted(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	err := as.IsAdminPermitted(&AuthInfo{Username: "root", Revision: 1})
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}

	// invalid user
	err = as.IsAdminPermitted(&AuthInfo{Username: "rooti", Revision: 1})
	if err != ErrUserNotFound {
		t.Errorf("expected %v, got %v", ErrUserNotFound, err)
	}

	// empty user
	err = as.IsAdminPermitted(&AuthInfo{Username: "", Revision: 1})
	if err != ErrUserEmpty {
		t.Errorf("expected %v, got %v", ErrUserEmpty, err)
	}

	// non-admin user
	err = as.IsAdminPermitted(&AuthInfo{Username: "foo", Revision: 1})
	if err != ErrPermissionDenied {
		t.Errorf("expected %v, got %v", ErrPermissionDenied, err)
	}

	// disabled auth should return nil
	as.AuthDisable()
	err = as.IsAdminPermitted(&AuthInfo{Username: "root", Revision: 1})
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestRecoverFromSnapshot(t *testing.T) {
	as, teardown := setupAuthStore(t)
	defer teardown(t)

	ua := &pb.AuthUserAddRequest{Name: "foo", Options: &authpb.UserAddOptions{NoPassword: false}}
	_, err := as.UserAdd(ua) // add an existing user
	if err == nil {
		t.Fatalf("expected %v, got %v", ErrUserAlreadyExist, err)
	}
	if err != ErrUserAlreadyExist {
		t.Fatalf("expected %v, got %v", ErrUserAlreadyExist, err)
	}

	ua = &pb.AuthUserAddRequest{Name: "", Options: &authpb.UserAddOptions{NoPassword: false}}
	_, err = as.UserAdd(ua) // add a user with empty name
	if err != ErrUserEmpty {
		t.Fatal(err)
	}

	as.Close()

	tp, err := NewTokenProvider(zap.NewExample(), tokenTypeSimple, dummyIndexWaiter, simpleTokenTTLDefault)
	if err != nil {
		t.Fatal(err)
	}
	as2 := NewAuthStore(zap.NewExample(), as.be, tp, bcrypt.MinCost)
	defer as2.Close()

	if !as2.IsAuthEnabled() {
		t.Fatal("recovering authStore from existing backend failed")
	}

	ul, err := as.UserList(&pb.AuthUserListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(ul.Users, "root") {
		t.Errorf("expected %v in %v", "root", ul.Users)
	}
}

func contains(array []string, str string) bool {
	for _, s := range array {
		if s == str {
			return true
		}
	}
	return false
}

func TestHammerSimpleAuthenticate(t *testing.T) {
	// set TTL values low to try to trigger races
	oldTTL, oldTTLRes := simpleTokenTTLDefault, simpleTokenTTLResolution
	defer func() {
		simpleTokenTTLDefault = oldTTL
		simpleTokenTTLResolution = oldTTLRes
	}()
	simpleTokenTTLDefault = 10 * time.Millisecond
	simpleTokenTTLResolution = simpleTokenTTLDefault
	users := make(map[string]struct{})

	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	// create lots of users
	for i := 0; i < 50; i++ {
		u := fmt.Sprintf("user-%d", i)
		ua := &pb.AuthUserAddRequest{Name: u, HashedPassword: encodePassword("123"), Options: &authpb.UserAddOptions{NoPassword: false}}
		if _, err := as.UserAdd(ua); err != nil {
			t.Fatal(err)
		}
		users[u] = struct{}{}
	}

	// hammer on authenticate with lots of users
	for i := 0; i < 10; i++ {
		var wg sync.WaitGroup
		wg.Add(len(users))
		for u := range users {
			go func(user string) {
				defer wg.Done()
				token := fmt.Sprintf("%s(%d)", user, i)
				ctx := context.WithValue(context.WithValue(context.TODO(), AuthenticateParamIndex{}, uint64(1)), AuthenticateParamSimpleTokenPrefix{}, token)
				if _, err := as.Authenticate(ctx, user, "123"); err != nil {
					t.Error(err)
				}
				if _, err := as.AuthInfoFromCtx(ctx); err != nil {
					t.Error(err)
				}
			}(u)
		}
		time.Sleep(time.Millisecond)
		wg.Wait()
	}
}

// TestRolesOrder tests authpb.User.Roles is sorted
func TestRolesOrder(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, b)

	tp, err := NewTokenProvider(zap.NewExample(), tokenTypeSimple, dummyIndexWaiter, simpleTokenTTLDefault)
	defer tp.disable()
	if err != nil {
		t.Fatal(err)
	}
	as := NewAuthStore(zap.NewExample(), b, tp, bcrypt.MinCost)
	defer as.Close()
	err = enableAuthAndCreateRoot(as)
	if err != nil {
		t.Fatal(err)
	}

	username := "user"
	_, err = as.UserAdd(&pb.AuthUserAddRequest{Name: username, HashedPassword: encodePassword("pass"), Options: &authpb.UserAddOptions{NoPassword: false}})
	if err != nil {
		t.Fatal(err)
	}

	roles := []string{"role1", "role2", "abc", "xyz", "role3"}
	for _, role := range roles {
		_, err = as.RoleAdd(&pb.AuthRoleAddRequest{Name: role})
		if err != nil {
			t.Fatal(err)
		}

		_, err = as.UserGrantRole(&pb.AuthUserGrantRoleRequest{User: username, Role: role})
		if err != nil {
			t.Fatal(err)
		}
	}

	user, err := as.UserGet(&pb.AuthUserGetRequest{Name: username})
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i < len(user.Roles); i++ {
		if strings.Compare(user.Roles[i-1], user.Roles[i]) != -1 {
			t.Errorf("User.Roles isn't sorted (%s vs %s)", user.Roles[i-1], user.Roles[i])
		}
	}
}

func TestAuthInfoFromCtxWithRootSimple(t *testing.T) {
	testAuthInfoFromCtxWithRoot(t, tokenTypeSimple)
}

func TestAuthInfoFromCtxWithRootJWT(t *testing.T) {
	opts := testJWTOpts()
	testAuthInfoFromCtxWithRoot(t, opts)
}

// testAuthInfoFromCtxWithRoot ensures "WithRoot" properly embeds token in the context.
func testAuthInfoFromCtxWithRoot(t *testing.T, opts string) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, b)

	tp, err := NewTokenProvider(zap.NewExample(), opts, dummyIndexWaiter, simpleTokenTTLDefault)
	if err != nil {
		t.Fatal(err)
	}
	as := NewAuthStore(zap.NewExample(), b, tp, bcrypt.MinCost)
	defer as.Close()

	if err = enableAuthAndCreateRoot(as); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	ctx = as.WithRoot(ctx)

	ai, aerr := as.AuthInfoFromCtx(ctx)
	if aerr != nil {
		t.Error(err)
	}
	if ai == nil {
		t.Error("expected non-nil *AuthInfo")
	}
	if ai.Username != "root" {
		t.Errorf("expected user name 'root', got %+v", ai)
	}
}

func TestUserNoPasswordAdd(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	username := "usernopass"
	ua := &pb.AuthUserAddRequest{Name: username, Options: &authpb.UserAddOptions{NoPassword: true}}
	_, err := as.UserAdd(ua)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.WithValue(context.WithValue(context.TODO(), AuthenticateParamIndex{}, uint64(1)), AuthenticateParamSimpleTokenPrefix{}, "dummy")
	_, err = as.Authenticate(ctx, username, "")
	if err != ErrAuthFailed {
		t.Fatalf("expected %v, got %v", ErrAuthFailed, err)
	}
}

func TestUserAddWithOldLog(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	ua := &pb.AuthUserAddRequest{Name: "bar", Password: "baz", Options: &authpb.UserAddOptions{NoPassword: false}}
	_, err := as.UserAdd(ua)
	if err != nil {
		t.Fatal(err)
	}
}

func TestUserChangePasswordWithOldLog(t *testing.T) {
	as, tearDown := setupAuthStore(t)
	defer tearDown(t)

	ctx1 := context.WithValue(context.WithValue(context.TODO(), AuthenticateParamIndex{}, uint64(1)), AuthenticateParamSimpleTokenPrefix{}, "dummy")
	_, err := as.Authenticate(ctx1, "foo", "bar")
	if err != nil {
		t.Fatal(err)
	}

	_, err = as.UserChangePassword(&pb.AuthUserChangePasswordRequest{Name: "foo", Password: "baz"})
	if err != nil {
		t.Fatal(err)
	}

	ctx2 := context.WithValue(context.WithValue(context.TODO(), AuthenticateParamIndex{}, uint64(2)), AuthenticateParamSimpleTokenPrefix{}, "dummy")
	_, err = as.Authenticate(ctx2, "foo", "baz")
	if err != nil {
		t.Fatal(err)
	}

	// change a non-existing user
	_, err = as.UserChangePassword(&pb.AuthUserChangePasswordRequest{Name: "foo-test", HashedPassword: encodePassword("bar")})
	if err == nil {
		t.Fatalf("expected %v, got %v", ErrUserNotFound, err)
	}
	if err != ErrUserNotFound {
		t.Fatalf("expected %v, got %v", ErrUserNotFound, err)
	}
}

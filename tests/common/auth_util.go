// Copyright 2022 The etcd Authors
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

package common

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/api/v3/authpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/interfaces"
)

const (
	rootUserName = "root"
	rootRoleName = "root"
	rootPassword = "rootPassword"
	testUserName = "test-user"
	testRoleName = "test-role"
	testPassword = "pass"
)

var (
	rootUser = authUser{user: rootUserName, pass: rootPassword, role: rootRoleName}
	testUser = authUser{user: testUserName, pass: testPassword, role: testRoleName}

	testRole = authRole{
		role:       testRoleName,
		permission: clientv3.PermissionType(clientv3.PermReadWrite),
		key:        "foo",
		keyEnd:     "",
	}
)

type authRole struct {
	role       string
	permission clientv3.PermissionType
	key        string
	keyEnd     string
}

type authUser struct {
	user string
	pass string
	role string
}

func createRoles(c interfaces.Client, roles []authRole) error {
	for _, r := range roles {
		// add role
		if _, err := c.RoleAdd(context.TODO(), r.role); err != nil {
			return fmt.Errorf("RoleAdd failed: %w", err)
		}

		// grant permission to role
		if _, err := c.RoleGrantPermission(context.TODO(), r.role, r.key, r.keyEnd, r.permission); err != nil {
			return fmt.Errorf("RoleGrantPermission failed: %w", err)
		}
	}

	return nil
}

func createUsers(c interfaces.Client, users []authUser) error {
	for _, u := range users {
		// add user
		if _, err := c.UserAdd(context.TODO(), u.user, u.pass, config.UserAddOptions{}); err != nil {
			return fmt.Errorf("UserAdd failed: %w", err)
		}

		// grant role to user
		if _, err := c.UserGrantRole(context.TODO(), u.user, u.role); err != nil {
			return fmt.Errorf("UserGrantRole failed: %w", err)
		}
	}

	return nil
}

func createSignedJWT(keyPath, alg, username string, authRevision uint64, expires int64) (string, error) {
	signMethod := jwt.GetSigningMethod(alg)

	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return "", err
	}

	key, err := jwt.ParseRSAPrivateKeyFromPEM(keyBytes)
	if err != nil {
		return "", err
	}

	if expires <= 0 {
		expires = time.Now().Add(time.Minute).Unix()
	}

	tk := jwt.NewWithClaims(signMethod,
		jwt.MapClaims{
			"username": username,
			"revision": authRevision,
			"exp":      expires,
		})

	return tk.SignedString(key)
}

func setupAuth(c interfaces.Client, roles []authRole, users []authUser) error {
	// create roles
	if err := createRoles(c, roles); err != nil {
		return err
	}

	if err := createUsers(c, users); err != nil {
		return err
	}

	// enable auth
	return c.AuthEnable(context.TODO())
}

func setupAuthAndGetRevision(c interfaces.Client, roles []authRole, users []authUser) (uint64, error) {
	// create roles
	if err := createRoles(c, roles); err != nil {
		return 0, err
	}

	if err := createUsers(c, users); err != nil {
		return 0, err
	}

	// Retrieve the current auth revision after creating roles and users but before enabling auth
	authrev, err := c.AuthStatus(context.TODO())
	if err != nil {
		return 0, err
	}

	// enable auth
	return authrev.AuthRevision, c.AuthEnable(context.TODO())
}

func requireRolePermissionEqual(t *testing.T, expectRole authRole, actual []*authpb.Permission) {
	require.Len(t, actual, 1)
	require.Equal(t, expectRole.permission, clientv3.PermissionType(actual[0].PermType))
	require.Equal(t, expectRole.key, string(actual[0].Key))
	require.Equal(t, expectRole.keyEnd, string(actual[0].RangeEnd))
}

func requireUserRolesEqual(t *testing.T, expectUser authUser, actual []string) {
	require.Len(t, actual, 1)
	require.Equal(t, expectUser.role, actual[0])
}

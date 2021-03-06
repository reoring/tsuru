// Copyright 2015 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tsuru/config"
	"github.com/tsuru/tsuru/app/bind"
	"github.com/tsuru/tsuru/auth"
	"github.com/tsuru/tsuru/errors"
	"github.com/tsuru/tsuru/permission"
	"github.com/tsuru/tsuru/provision"
	"github.com/tsuru/tsuru/provision/provisiontest"
	"github.com/tsuru/tsuru/quota"
	"github.com/tsuru/tsuru/repository"
	"github.com/tsuru/tsuru/repository/repositorytest"
	"github.com/tsuru/tsuru/router/routertest"
	"github.com/tsuru/tsuru/safe"
	"github.com/tsuru/tsuru/service"
	"github.com/tsuru/tsuru/tsurutest"
	"gopkg.in/check.v1"
	"gopkg.in/mgo.v2/bson"
)

func (s *S) TestGetAppByName(c *check.C) {
	newApp := App{Name: "myApp", Platform: "Django"}
	err := s.conn.Apps().Insert(newApp)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": newApp.Name})
	newApp.Env = map[string]bind.EnvVar{}
	err = s.conn.Apps().Update(bson.M{"name": newApp.Name}, &newApp)
	c.Assert(err, check.IsNil)
	myApp, err := GetByName("myApp")
	c.Assert(err, check.IsNil)
	c.Assert(myApp.Name, check.Equals, newApp.Name)
}

func (s *S) TestGetAppByNameNotFound(c *check.C) {
	app, err := GetByName("wat")
	c.Assert(err, check.Equals, ErrAppNotFound)
	c.Assert(app, check.IsNil)
}

func (s *S) TestDelete(c *check.C) {
	s.conn.Users().Update(
		bson.M{"email": s.user.Email},
		bson.M{"$set": bson.M{"quota.limit": 1, "quota.inuse": 1}},
	)
	defer s.conn.Users().Update(
		bson.M{"email": s.user.Email},
		bson.M{"$set": bson.M{"quota": quota.Unlimited}},
	)
	a := App{
		Name:     "ritual",
		Platform: "ruby",
		Owner:    s.user.Email,
	}
	err := repository.Manager().CreateRepository(a.Name, nil)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	app, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	err = app.Log("msg", "src", "unit")
	c.Assert(err, check.IsNil)
	err = Delete(app, nil)
	c.Assert(err, check.IsNil)
	_, err = GetByName(app.Name)
	c.Assert(err, check.Equals, ErrAppNotFound)
	c.Assert(s.provisioner.Provisioned(&a), check.Equals, false)
	err = auth.ReserveApp(s.user)
	c.Assert(err, check.IsNil)
	count, err := s.logConn.Logs(app.Name).Count()
	c.Assert(err, check.IsNil)
	c.Assert(count, check.Equals, 0)
	_, err = repository.Manager().GetRepository(a.Name)
	c.Assert(err, check.NotNil)
	c.Assert(err.Error(), check.Equals, "repository not found")
}

func (s *S) TestDeleteWithDeploys(c *check.C) {
	a := App{
		Name:      "ritual",
		Platform:  "python",
		TeamOwner: s.team.Name,
	}
	err := CreateApp(&a, s.user)
	c.Assert(err, check.IsNil)
	app, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	err = s.conn.Deploys().Insert(DeployData{App: a.Name, Timestamp: time.Now()})
	c.Assert(err, check.IsNil)
	defer s.conn.Deploys().RemoveAll(bson.M{"app": a.Name})
	Delete(app, nil)
	err = tsurutest.WaitCondition(1e9, func() bool {
		var deploys []DeployData
		err := s.conn.Deploys().Find(bson.M{"app": app.Name}).All(&deploys)
		if err != nil {
			c.Log(err)
			return false
		}
		return len(deploys) == 1 && !deploys[0].RemoveDate.IsZero()
	})
	c.Assert(err, check.IsNil)
	_, err = repository.Manager().GetRepository(a.Name)
	c.Assert(err, check.NotNil)
	c.Assert(err.Error(), check.Equals, "repository not found")
}

func (s *S) TestDeleteWithoutUnits(c *check.C) {
	app := App{Name: "x4", Platform: "python", TeamOwner: s.team.Name}
	err := CreateApp(&app, s.user)
	c.Assert(err, check.IsNil)
	defer s.provisioner.Destroy(&app)
	a, err := GetByName(app.Name)
	c.Assert(err, check.IsNil)
	Delete(a, nil)
	_, err = repository.Manager().GetRepository(app.Name)
	c.Assert(err, check.NotNil)
	c.Assert(err.Error(), check.Equals, "repository not found")
}

func (s *S) TestDeleteSwappedApp(c *check.C) {
	s.conn.Users().Update(
		bson.M{"email": s.user.Email},
		bson.M{"$set": bson.M{"quota.limit": 1, "quota.inuse": 1}},
	)
	defer s.conn.Users().Update(
		bson.M{"email": s.user.Email},
		bson.M{"$set": bson.M{"quota": quota.Unlimited}},
	)
	a := App{
		Name:     "ritual",
		Platform: "ruby",
		Owner:    s.user.Email,
	}
	err := s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	app2 := &App{Name: "app2"}
	err = s.conn.Apps().Insert(app2)
	c.Assert(err, check.IsNil)
	err = s.provisioner.Provision(app2)
	c.Assert(err, check.IsNil)
	err = Swap(&a, app2)
	c.Assert(err, check.IsNil)
	err = Delete(&a, nil)
	c.Assert(err, check.ErrorMatches, "application is swapped with \"app2\", cannot remove it")
	c.Assert(s.provisioner.Provisioned(&a), check.Equals, true)
}

func (s *S) TestCreateApp(c *check.C) {
	a := App{
		Name:      "appname",
		Platform:  "python",
		TeamOwner: s.team.Name,
	}
	expectedHost := "localhost"
	config.Set("host", expectedHost)
	s.conn.Users().Update(bson.M{"email": s.user.Email}, bson.M{"$set": bson.M{"quota.limit": 1}})
	defer s.conn.Users().Update(bson.M{"email": s.user.Email}, bson.M{"$set": bson.M{"quota.limit": -1}})
	config.Set("quota:units-per-app", 3)
	defer config.Unset("quota:units-per-app")
	err := CreateApp(&a, s.user)
	c.Assert(err, check.IsNil)
	defer Delete(&a, nil)
	retrievedApp, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(retrievedApp.Name, check.Equals, a.Name)
	c.Assert(retrievedApp.Platform, check.Equals, a.Platform)
	c.Assert(retrievedApp.Teams, check.DeepEquals, []string{s.team.Name})
	c.Assert(retrievedApp.Owner, check.Equals, s.user.Email)
	env := retrievedApp.InstanceEnv("")
	c.Assert(env["TSURU_APPNAME"].Value, check.Equals, a.Name)
	c.Assert(env["TSURU_APPNAME"].Public, check.Equals, false)
	err = auth.ReserveApp(s.user)
	_, ok := err.(*quota.QuotaExceededError)
	c.Assert(ok, check.Equals, true)
	_, err = repository.Manager().GetRepository(a.Name)
	c.Assert(err, check.IsNil)
}

func (s *S) TestCreateAppDefaultPlan(c *check.C) {
	a := App{
		Name:      "appname",
		Platform:  "python",
		TeamOwner: s.team.Name,
	}
	expectedHost := "localhost"
	config.Set("host", expectedHost)
	s.conn.Users().Update(bson.M{"email": s.user.Email}, bson.M{"$set": bson.M{"quota.limit": 1}})
	defer s.conn.Users().Update(bson.M{"email": s.user.Email}, bson.M{"$set": bson.M{"quota.limit": -1}})
	config.Set("quota:units-per-app", 3)
	defer config.Unset("quota:units-per-app")
	err := CreateApp(&a, s.user)
	c.Assert(err, check.IsNil)
	defer Delete(&a, nil)
	retrievedApp, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(retrievedApp.Plan, check.DeepEquals, s.defaultPlan)
	_, err = repository.Manager().GetRepository(a.Name)
	c.Assert(err, check.IsNil)
}

func (s *S) TestCreateAppWithoutDefaultPlan(c *check.C) {
	s.conn.Plans().RemoveAll(nil)
	defer s.conn.Plans().Insert(s.defaultPlan)
	a := App{
		Name:      "appname",
		Platform:  "python",
		TeamOwner: s.team.Name,
	}
	expectedHost := "localhost"
	config.Set("host", expectedHost)
	s.conn.Users().Update(bson.M{"email": s.user.Email}, bson.M{"$set": bson.M{"quota.limit": 1}})
	defer s.conn.Users().Update(bson.M{"email": s.user.Email}, bson.M{"$set": bson.M{"quota.limit": -1}})
	config.Set("quota:units-per-app", 3)
	defer config.Unset("quota:units-per-app")
	err := CreateApp(&a, s.user)
	c.Assert(err, check.IsNil)
	defer Delete(&a, nil)
	retrievedApp, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(retrievedApp.Plan, check.DeepEquals, Plan{
		Name:     "autogenerated",
		Memory:   0,
		Swap:     0,
		CpuShare: 100,
	})
	_, err = repository.Manager().GetRepository(a.Name)
	c.Assert(err, check.IsNil)
}

func (s *S) TestCreateAppWithExplicitPlan(c *check.C) {
	myPlan := Plan{
		Name:     "myplan",
		Memory:   1,
		Swap:     2,
		CpuShare: 3,
	}
	err := myPlan.Save()
	c.Assert(err, check.IsNil)
	defer PlanRemove(myPlan.Name)
	a := App{
		Name:      "appname",
		Platform:  "python",
		Plan:      Plan{Name: "myplan"},
		TeamOwner: s.team.Name,
	}
	expectedHost := "localhost"
	config.Set("host", expectedHost)
	s.conn.Users().Update(bson.M{"email": s.user.Email}, bson.M{"$set": bson.M{"quota.limit": 1}})
	defer s.conn.Users().Update(bson.M{"email": s.user.Email}, bson.M{"$set": bson.M{"quota.limit": -1}})
	config.Set("quota:units-per-app", 3)
	defer config.Unset("quota:units-per-app")
	err = CreateApp(&a, s.user)
	c.Assert(err, check.IsNil)
	defer Delete(&a, nil)
	retrievedApp, err := GetByName(a.Name)
	c.Assert(retrievedApp.Plan, check.DeepEquals, myPlan)
	_, err = repository.Manager().GetRepository(a.Name)
	c.Assert(err, check.IsNil)
}

func (s *S) TestCreateAppUserQuotaExceeded(c *check.C) {
	app := App{Name: "america", Platform: "python", TeamOwner: s.team.Name}
	s.conn.Users().Update(
		bson.M{"email": s.user.Email},
		bson.M{"$set": bson.M{"quota.limit": 0}},
	)
	defer s.conn.Users().Update(
		bson.M{"email": s.user.Email},
		bson.M{"$set": bson.M{"quota.limit": -1}},
	)
	err := CreateApp(&app, s.user)
	e, ok := err.(*AppCreationError)
	c.Assert(ok, check.Equals, true)
	_, ok = e.Err.(*quota.QuotaExceededError)
	c.Assert(ok, check.Equals, true)
}

func (s *S) TestCreateAppTeamOwner(c *check.C) {
	app := App{Name: "america", Platform: "python", TeamOwner: "tsuruteam"}
	err := CreateApp(&app, s.user)
	c.Assert(err, check.IsNil)
	defer Delete(&app, nil)
	c.Assert(app.TeamOwner, check.Equals, "tsuruteam")
}

func (s *S) TestCreateAppTeamOwnerTeamNotFound(c *check.C) {
	app := App{
		Name:      "someapp",
		Platform:  "python",
		TeamOwner: "not found",
	}
	err := CreateApp(&app, s.user)
	c.Assert(err, check.NotNil)
	c.Assert(err.Error(), check.Equals, "team not found")
}

func (s *S) TestCannotCreateAppWithoutTeamOwner(c *check.C) {
	u := auth.User{Email: "perpetual@yes.com"}
	err := u.Create()
	c.Assert(err, check.IsNil)
	defer s.conn.Users().Remove(bson.M{"email": u.Email})
	a := App{Name: "beyond"}
	err = CreateApp(&a, &u)
	c.Check(err, check.Equals, auth.ErrTeamNotFound)
}

func (s *S) TestCantCreateTwoAppsWithTheSameName(c *check.C) {
	err := s.conn.Apps().Insert(bson.M{"name": "appname"})
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": "appname"})
	a := App{Name: "appname", Platform: "python", TeamOwner: s.team.Name}
	err = CreateApp(&a, s.user)
	defer Delete(&a, nil) // clean mess if test fail
	c.Assert(err, check.NotNil)
	e, ok := err.(*AppCreationError)
	c.Assert(ok, check.Equals, true)
	c.Assert(e.app, check.Equals, "appname")
	c.Assert(e.Err, check.NotNil)
	c.Assert(e.Err.Error(), check.Equals, "there is already an app with this name")
}

func (s *S) TestCantCreateAppWithInvalidName(c *check.C) {
	a := App{
		Name:      "1123app",
		Platform:  "python",
		TeamOwner: s.team.Name,
	}
	err := CreateApp(&a, s.user)
	c.Assert(err, check.NotNil)
	e, ok := err.(*errors.ValidationError)
	c.Assert(ok, check.Equals, true)
	msg := "Invalid app name, your app should have at most 63 " +
		"characters, containing only lower case letters, numbers or dashes, " +
		"starting with a letter."
	c.Assert(e.Message, check.Equals, msg)
}

func (s *S) TestCreateAppProvisionerFailures(c *check.C) {
	s.provisioner.PrepareFailure("Provision", fmt.Errorf("exit status 1"))
	a := App{
		Name:      "theirapp",
		Platform:  "python",
		TeamOwner: s.team.Name,
	}
	err := CreateApp(&a, s.user)
	defer Delete(&a, nil) // clean mess if test fail
	c.Assert(err, check.NotNil)
	expected := `tsuru failed to create the app "theirapp": exit status 1`
	c.Assert(err.Error(), check.Equals, expected)
	_, err = GetByName(a.Name)
	c.Assert(err, check.NotNil)
}

func (s *S) TestCreateAppRepositoryManagerFailure(c *check.C) {
	repository.Manager().CreateRepository("otherapp", nil)
	a := App{Name: "otherapp", Platform: "python"}
	err := CreateApp(&a, s.user)
	c.Assert(err, check.NotNil)
	count, err := s.conn.Apps().Find(bson.M{"name": a.Name}).Count()
	c.Assert(err, check.IsNil)
	c.Assert(count, check.Equals, 0)
}

func (s *S) TestBindAndUnbindUnit(c *check.C) {
	var requests []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r)
	}))
	defer server.Close()
	app := App{
		Name: "warpaint", Platform: "python",
		Quota: quota.Unlimited,
	}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	s.provisioner.Provision(&app)
	defer s.provisioner.Destroy(&app)
	srvc := service.Service{
		Name:     "mysql",
		Endpoint: map[string]string{"production": server.URL},
	}
	err = srvc.Create()
	c.Assert(err, check.IsNil)
	defer srvc.Delete()
	si1 := service.ServiceInstance{
		Name:        "mydb",
		ServiceName: "mysql",
		Apps:        []string{app.Name},
	}
	err = si1.Create()
	c.Assert(err, check.IsNil)
	defer s.conn.ServiceInstances().Remove(bson.M{"name": si1.Name})
	si2 := service.ServiceInstance{
		Name:        "yourdb",
		ServiceName: "mysql",
		Apps:        []string{app.Name},
	}
	err = si2.Create()
	c.Assert(err, check.IsNil)
	defer s.conn.ServiceInstances().Remove(bson.M{"name": si2.Name})
	unit := provision.Unit{ID: "some-unit", Ip: "127.0.2.1"}
	err = app.BindUnit(&unit)
	c.Assert(err, check.IsNil)
	err = app.UnbindUnit(&unit)
	c.Assert(err, check.IsNil)
	c.Assert(requests, check.HasLen, 4)
	c.Assert(requests[0].Method, check.Equals, "POST")
	c.Assert(requests[0].URL.Path, check.Equals, "/resources/mydb/bind")
	c.Assert(requests[1].Method, check.Equals, "POST")
	c.Assert(requests[1].URL.Path, check.Equals, "/resources/yourdb/bind")
	c.Assert(requests[2].Method, check.Equals, "DELETE")
	c.Assert(requests[2].URL.Path, check.Equals, "/resources/mydb/bind")
	c.Assert(requests[3].Method, check.Equals, "DELETE")
	c.Assert(requests[3].URL.Path, check.Equals, "/resources/yourdb/bind")
}

func (s *S) TestAddUnits(c *check.C) {
	app := App{
		Name: "warpaint", Platform: "python",
		Quota: quota.Unlimited,
	}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	s.provisioner.Provision(&app)
	defer s.provisioner.Destroy(&app)
	err = app.AddUnits(5, "web", nil)
	c.Assert(err, check.IsNil)
	units, err := app.Units()
	c.Assert(err, check.IsNil)
	c.Assert(units, check.HasLen, 5)
	err = app.AddUnits(2, "worker", nil)
	c.Assert(err, check.IsNil)
	units, err = app.Units()
	c.Assert(err, check.IsNil)
	c.Assert(units, check.HasLen, 7)
	for i, unit := range units {
		c.Assert(unit.AppName, check.Equals, app.Name)
		if i < 5 {
			c.Assert(unit.ProcessName, check.Equals, "web")
		} else {
			c.Assert(unit.ProcessName, check.Equals, "worker")
		}
	}
}

func (s *S) TestAddUnitsWithWriter(c *check.C) {
	app := App{
		Name: "warpaint", Platform: "python",
		Quota: quota.Unlimited,
	}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	s.provisioner.Provision(&app)
	defer s.provisioner.Destroy(&app)
	var buf bytes.Buffer
	err = app.AddUnits(2, "web", &buf)
	c.Assert(err, check.IsNil)
	units, err := app.Units()
	c.Assert(err, check.IsNil)
	c.Assert(units, check.HasLen, 2)
	for _, unit := range units {
		c.Assert(unit.AppName, check.Equals, app.Name)
	}
	c.Assert(buf.String(), check.Equals, "added 2 units")
}

func (s *S) TestAddUnitsQuota(c *check.C) {
	app := App{
		Name: "warpaint", Platform: "python",
		Quota: quota.Quota{Limit: 7},
	}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	s.provisioner.Provision(&app)
	defer s.provisioner.Destroy(&app)
	otherApp := App{Name: "warpaint"}
	err = otherApp.AddUnits(5, "web", nil)
	c.Assert(err, check.IsNil)
	units := s.provisioner.GetUnits(&app)
	c.Assert(units, check.HasLen, 5)
	err = otherApp.AddUnits(2, "web", nil)
	c.Assert(err, check.IsNil)
	units = s.provisioner.GetUnits(&app)
	c.Assert(units, check.HasLen, 7)
	err = reserveUnits(&app, 1)
	_, ok := err.(*quota.QuotaExceededError)
	c.Assert(ok, check.Equals, true)
}

func (s *S) TestAddUnitsQuotaExceeded(c *check.C) {
	app := App{Name: "warpaint", Platform: "ruby"}
	s.conn.Apps().Insert(app)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	err := app.AddUnits(1, "web", nil)
	e, ok := err.(*quota.QuotaExceededError)
	c.Assert(ok, check.Equals, true)
	c.Assert(e.Available, check.Equals, uint(0))
	c.Assert(e.Requested, check.Equals, uint(1))
	units := s.provisioner.GetUnits(&app)
	c.Assert(units, check.HasLen, 0)
}

func (s *S) TestAddUnitsMultiple(c *check.C) {
	app := App{
		Name: "warpaint", Platform: "ruby",
		Quota: quota.Quota{Limit: 10},
	}
	s.conn.Apps().Insert(app)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	err := app.AddUnits(11, "web", nil)
	e, ok := err.(*quota.QuotaExceededError)
	c.Assert(ok, check.Equals, true)
	c.Assert(e.Available, check.Equals, uint(10))
	c.Assert(e.Requested, check.Equals, uint(11))
}

func (s *S) TestAddZeroUnits(c *check.C) {
	app := App{Name: "warpaint", Platform: "ruby"}
	err := app.AddUnits(0, "web", nil)
	c.Assert(err, check.NotNil)
	c.Assert(err.Error(), check.Equals, "Cannot add zero units.")
}

func (s *S) TestAddUnitsFailureInProvisioner(c *check.C) {
	app := App{Name: "scars", Platform: "golang", Quota: quota.Unlimited}
	s.conn.Apps().Insert(app)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	err := app.AddUnits(2, "web", nil)
	c.Assert(err, check.NotNil)
	c.Assert(err.Error(), check.Equals, "App is not provisioned.")
}

func (s *S) TestAddUnitsIsAtomic(c *check.C) {
	app := App{
		Name: "warpaint", Platform: "golang",
		Quota: quota.Unlimited,
	}
	err := app.AddUnits(2, "web", nil)
	c.Assert(err, check.NotNil)
	_, err = GetByName(app.Name)
	c.Assert(err, check.Equals, ErrAppNotFound)
}

func (s *S) TestRemoveUnitsWithQuota(c *check.C) {
	a := App{
		Name:  "ble",
		Quota: quota.Quota{Limit: 6, InUse: 6},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 5, "web", nil)
	defer s.provisioner.Destroy(&a)
	err = a.RemoveUnits(4, "web", nil)
	c.Assert(err, check.IsNil)
	err = tsurutest.WaitCondition(2e9, func() bool {
		app, appErr := GetByName(a.Name)
		if appErr != nil {
			c.Log(appErr)
			return false
		}
		return app.Quota.InUse == 1
	})
	c.Assert(err, check.IsNil)
}

func (s *S) TestRemoveUnits(c *check.C) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	srvc := service.Service{Name: "mysql", Endpoint: map[string]string{"production": ts.URL}}
	err := srvc.Create()
	c.Assert(err, check.IsNil)
	defer s.conn.Services().Remove(bson.M{"_id": "mysql"})
	app := App{
		Name:     "chemistry",
		Platform: "python",
		Quota:    quota.Unlimited,
	}
	instance := service.ServiceInstance{
		Name:        "my-inst",
		ServiceName: "mysql",
		Teams:       []string{s.team.Name},
		Apps:        []string{app.Name},
	}
	instance.Create()
	defer s.conn.ServiceInstances().Remove(bson.M{"name": "my-inst"})
	err = s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	c.Assert(err, check.IsNil)
	s.provisioner.Provision(&app)
	defer s.provisioner.Destroy(&app)
	err = app.AddUnits(2, "web", nil)
	c.Assert(err, check.IsNil)
	err = app.AddUnits(2, "worker", nil)
	c.Assert(err, check.IsNil)
	err = app.AddUnits(2, "web", nil)
	c.Assert(err, check.IsNil)
	buf := bytes.NewBuffer(nil)
	err = app.RemoveUnits(2, "worker", buf)
	c.Assert(err, check.IsNil)
	c.Assert(buf.String(), check.Equals, "removing 2 units")
	err = tsurutest.WaitCondition(2e9, func() bool {
		gotApp, inErr := GetByName(app.Name)
		if inErr != nil {
			c.Log(inErr)
			return false
		}
		units, inErr := app.Units()
		c.Assert(inErr, check.IsNil)
		return len(units) == 4 && gotApp.Quota.InUse == 4
	})
	c.Assert(err, check.IsNil)
	ts.Close()
	units, err := app.Units()
	c.Assert(err, check.IsNil)
	for _, unit := range units {
		c.Assert(unit.ProcessName, check.Equals, "web")
	}
}

func (s *S) TestRemoveUnitsInvalidValues(c *check.C) {
	var tests = []struct {
		n        uint
		expected string
	}{
		{0, "cannot remove 0 units"},
		{4, "too many units to remove"},
	}
	app := App{
		Name:     "chemistryii",
		Platform: "python",
	}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	s.provisioner.Provision(&app)
	defer s.provisioner.Destroy(&app)
	s.provisioner.AddUnits(&app, 3, "web", nil)
	for _, test := range tests {
		err := app.RemoveUnits(test.n, "web", nil)
		c.Check(err, check.ErrorMatches, test.expected)
	}
}

func (s *S) TestSetUnitStatus(c *check.C) {
	a := App{Name: "appName", Platform: "python"}
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 3, "web", nil)
	units, err := a.Units()
	c.Assert(err, check.IsNil)
	err = a.SetUnitStatus(units[0].ID, provision.StatusError)
	c.Assert(err, check.IsNil)
	units, err = a.Units()
	c.Assert(err, check.IsNil)
	c.Assert(units[0].Status, check.Equals, provision.StatusError)
}

func (s *S) TestSetUnitStatusPartialID(c *check.C) {
	a := App{Name: "appName", Platform: "python"}
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 3, "web", nil)
	units, err := a.Units()
	c.Assert(err, check.IsNil)
	name := units[0].ID
	err = a.SetUnitStatus(name[0:len(name)-2], provision.StatusError)
	c.Assert(err, check.IsNil)
	units, err = a.Units()
	c.Assert(err, check.IsNil)
	c.Assert(units[0].Status, check.Equals, provision.StatusError)
}

func (s *S) TestSetUnitStatusNotFound(c *check.C) {
	a := App{Name: "appName", Platform: "django"}
	err := a.SetUnitStatus("someunit", provision.StatusError)
	c.Assert(err, check.NotNil)
	e, ok := err.(*provision.UnitNotFoundError)
	c.Assert(ok, check.Equals, true)
	c.Assert(e.ID, check.Equals, "someunit")
}

func (s *S) TestUpdateUnitsStatus(c *check.C) {
	a := App{Name: "lapname", Platform: "python"}
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 3, "web", nil)
	units, err := a.Units()
	c.Assert(err, check.IsNil)
	unitStates := []UpdateUnitsData{
		{ID: units[0].ID, Status: provision.Status("started")},
		{ID: units[1].ID, Status: provision.Status("stopped")},
		{ID: units[2].ID, Status: provision.Status("error")},
		{ID: units[2].ID + "-not-found", Status: provision.Status("error")},
	}
	result, err := UpdateUnitsStatus(unitStates)
	c.Assert(err, check.IsNil)
	expected := []UpdateUnitsResult{
		{ID: units[0].ID, Found: true},
		{ID: units[1].ID, Found: true},
		{ID: units[2].ID, Found: true},
		{ID: units[2].ID + "-not-found", Found: false},
	}
	c.Assert(result, check.DeepEquals, expected)
}

func (s *S) TestGrantAccess(c *check.C) {
	user := customUserWithPermission(c, "myuser", permission.Permission{
		Scheme:  permission.PermAppDeploy,
		Context: permission.Context(permission.CtxTeam, s.team.Name),
	})
	app := App{Name: "appName", Platform: "django", Teams: []string{"acid-rain", "zito"}}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	err = repository.Manager().CreateRepository(app.Name, nil)
	c.Assert(err, check.IsNil)
	err = app.Grant(&s.team)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Find(bson.M{"name": app.Name}).One(&app)
	c.Assert(err, check.IsNil)
	c.Assert(app.Teams, check.DeepEquals, []string{"acid-rain", "zito", s.team.Name})
	grants, err := repositorytest.Granted(app.Name)
	c.Assert(err, check.IsNil)
	c.Assert(grants, check.DeepEquals, []string{user.Email})
}

func (s *S) TestGrantAccessFailsIfTheTeamAlreadyHasAccessToTheApp(c *check.C) {
	a := App{Name: "appName", Platform: "django", Teams: []string{s.team.Name}}
	err := a.Grant(&s.team)
	c.Assert(err, check.Equals, ErrAlreadyHaveAccess)
}

func (s *S) TestRevokeAccess(c *check.C) {
	user := customUserWithPermission(c, "myuser", permission.Permission{
		Scheme:  permission.PermAppDeploy,
		Context: permission.Context(permission.CtxTeam, s.team.Name),
	})
	team := auth.Team{Name: "abcd"}
	err := s.conn.Teams().Insert(team)
	c.Assert(err, check.IsNil)
	app := App{Name: "appName", Platform: "django", Teams: []string{s.team.Name, team.Name}}
	err = s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	err = repository.Manager().CreateRepository(app.Name, nil)
	c.Assert(err, check.IsNil)
	err = repository.Manager().GrantAccess(app.Name, user.Email)
	c.Assert(err, check.IsNil)
	err = app.Revoke(&s.team)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Find(bson.M{"name": app.Name}).One(&app)
	c.Assert(err, check.IsNil)
	_, found := app.findTeam(&s.team)
	c.Assert(found, check.Equals, false)
	grants, err := repositorytest.Granted(app.Name)
	c.Assert(err, check.IsNil)
	c.Assert(grants, check.HasLen, 0)
}

func (s *S) TestRevokeAccessKeepsUsersThatBelongToTwoTeams(c *check.C) {
	team := auth.Team{Name: "abcd"}
	err := s.conn.Teams().Insert(team)
	c.Assert(err, check.IsNil)
	user := customUserWithPermission(c, "myuser", permission.Permission{
		Scheme:  permission.PermAppDeploy,
		Context: permission.Context(permission.CtxTeam, s.team.Name),
	}, permission.Permission{
		Scheme:  permission.PermAppDeploy,
		Context: permission.Context(permission.CtxTeam, team.Name),
	})
	app := App{Name: "appName", Platform: "django", Teams: []string{s.team.Name, team.Name}}
	err = s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	err = repository.Manager().CreateRepository(app.Name, nil)
	c.Assert(err, check.IsNil)
	err = repository.Manager().GrantAccess(app.Name, user.Email)
	c.Assert(err, check.IsNil)
	err = app.Revoke(&team)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Find(bson.M{"name": app.Name}).One(&app)
	c.Assert(err, check.IsNil)
	_, found := app.findTeam(&team)
	c.Assert(found, check.Equals, false)
	grants, err := repositorytest.Granted(app.Name)
	c.Assert(err, check.IsNil)
	c.Assert(grants, check.DeepEquals, []string{user.Email})
}

func (s *S) TestRevokeAccessDoesntLeaveOrphanApps(c *check.C) {
	app := App{Name: "appName", Platform: "django", Teams: []string{s.team.Name}}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	err = repository.Manager().CreateRepository(app.Name, nil)
	c.Assert(err, check.IsNil)
	err = repository.Manager().GrantAccess(app.Name, s.user.Email)
	c.Assert(err, check.IsNil)
	err = app.Revoke(&s.team)
	c.Assert(err, check.Equals, ErrCannotOrphanApp)
}

func (s *S) TestRevokeAccessFailsIfTheTeamsDoesNotHaveAccessToTheApp(c *check.C) {
	a := App{Name: "appName", Platform: "django", Teams: []string{}}
	err := a.Revoke(&s.team)
	c.Assert(err, check.Equals, ErrNoAccess)
}

func (s *S) TestSetEnvNewAppsTheMapIfItIsNil(c *check.C) {
	a := App{Name: "how-many-more-times"}
	c.Assert(a.Env, check.IsNil)
	env := bind.EnvVar{Name: "PATH", Value: "/"}
	a.setEnv(env)
	c.Assert(a.Env, check.NotNil)
}

func (s *S) TestSetPublicEnvironmentVariableToApp(c *check.C) {
	a := App{Name: "appName", Platform: "django"}
	a.setEnv(bind.EnvVar{Name: "PATH", Value: "/", Public: true})
	env := a.Env["PATH"]
	c.Assert(env.Name, check.Equals, "PATH")
	c.Assert(env.Value, check.Equals, "/")
	c.Assert(env.Public, check.Equals, true)
}

func (s *S) TestSetPrivateEnvironmentVariableToApp(c *check.C) {
	a := App{Name: "appName", Platform: "django"}
	a.setEnv(bind.EnvVar{Name: "PATH", Value: "/", Public: false})
	env := a.Env["PATH"]
	c.Assert(env.Name, check.Equals, "PATH")
	c.Assert(env.Value, check.Equals, "/")
	c.Assert(env.Public, check.Equals, false)
}

func (s *S) TestSetMultiplePublicEnvironmentVariableToApp(c *check.C) {
	a := App{Name: "appName", Platform: "django"}
	a.setEnv(bind.EnvVar{Name: "PATH", Value: "/", Public: true})
	a.setEnv(bind.EnvVar{Name: "DATABASE", Value: "mongodb", Public: true})
	env := a.Env["PATH"]
	c.Assert(env.Name, check.Equals, "PATH")
	c.Assert(env.Value, check.Equals, "/")
	c.Assert(env.Public, check.Equals, true)
	env = a.Env["DATABASE"]
	c.Assert(env.Name, check.Equals, "DATABASE")
	c.Assert(env.Value, check.Equals, "mongodb")
	c.Assert(env.Public, check.Equals, true)
}

func (s *S) TestSetMultiplePrivateEnvironmentVariableToApp(c *check.C) {
	a := App{Name: "appName", Platform: "django"}
	a.setEnv(bind.EnvVar{Name: "PATH", Value: "/", Public: false})
	a.setEnv(bind.EnvVar{Name: "DATABASE", Value: "mongodb", Public: false})
	env := a.Env["PATH"]
	c.Assert(env.Name, check.Equals, "PATH")
	c.Assert(env.Value, check.Equals, "/")
	c.Assert(env.Public, check.Equals, false)
	env = a.Env["DATABASE"]
	c.Assert(env.Name, check.Equals, "DATABASE")
	c.Assert(env.Value, check.Equals, "mongodb")
	c.Assert(env.Public, check.Equals, false)
}

func (s *S) TestSetEnvRespectsThePublicOnlyFlagKeepPrivateVariablesWhenServiceSets(c *check.C) {
	a := App{
		Name: "myapp",
		Env: map[string]bind.EnvVar{
			"DATABASE_HOST": {
				Name:         "DATABASE_HOST",
				Value:        "localhost",
				Public:       false,
				InstanceName: "some service",
			},
		},
	}
	s.provisioner.PrepareOutput([]byte("exported"))
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	envs := []bind.EnvVar{
		{
			Name:   "DATABASE_HOST",
			Value:  "remotehost",
			Public: false,
		},
		{
			Name:   "DATABASE_PASSWORD",
			Value:  "123",
			Public: true,
		},
	}
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	var buf bytes.Buffer
	err = a.setEnvsToApp(
		bind.SetEnvApp{
			Envs:          envs,
			PublicOnly:    true,
			ShouldRestart: true,
		}, &buf)
	c.Assert(err, check.IsNil)
	newApp, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	expected := map[string]bind.EnvVar{
		"DATABASE_HOST": {
			Name:         "DATABASE_HOST",
			Value:        "localhost",
			Public:       false,
			InstanceName: "some service",
		},
		"DATABASE_PASSWORD": {
			Name:   "DATABASE_PASSWORD",
			Value:  "123",
			Public: true,
		},
	}
	c.Assert(newApp.Env, check.DeepEquals, expected)
	c.Assert(s.provisioner.Restarts(&a, ""), check.Equals, 1)
	c.Assert(buf.String(), check.Equals, "---- Setting 2 new environment variables ----\nrestarting app")
}

func (s *S) TestSetEnvRespectsThePublicOnlyFlagOverwrittenAllVariablesWhenItsFalse(c *check.C) {
	a := App{
		Name: "myapp",
		Env: map[string]bind.EnvVar{
			"DATABASE_HOST": {
				Name:   "DATABASE_HOST",
				Value:  "localhost",
				Public: false,
			},
		},
	}
	s.provisioner.PrepareOutput([]byte("exported"))
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	envs := []bind.EnvVar{
		{
			Name:   "DATABASE_HOST",
			Value:  "remotehost",
			Public: true,
		},
		{
			Name:   "DATABASE_PASSWORD",
			Value:  "123",
			Public: true,
		},
	}
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	err = a.setEnvsToApp(
		bind.SetEnvApp{
			Envs:          envs,
			PublicOnly:    false,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	newApp, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	expected := map[string]bind.EnvVar{
		"DATABASE_HOST": {
			Name:   "DATABASE_HOST",
			Value:  "remotehost",
			Public: true,
		},
		"DATABASE_PASSWORD": {
			Name:   "DATABASE_PASSWORD",
			Value:  "123",
			Public: true,
		},
	}
	c.Assert(newApp.Env, check.DeepEquals, expected)
	c.Assert(s.provisioner.Restarts(&a, ""), check.Equals, 1)
}

func (s *S) TestSetEnvWithNoRestartFlag(c *check.C) {
	a := App{
		Name: "myapp",
		Env: map[string]bind.EnvVar{
			"DATABASE_HOST": {
				Name:   "DATABASE_HOST",
				Value:  "localhost",
				Public: false,
			},
		},
	}
	s.provisioner.PrepareOutput([]byte("exported"))
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	envs := []bind.EnvVar{
		{
			Name:   "DATABASE_HOST",
			Value:  "remotehost",
			Public: true,
		},
		{
			Name:   "DATABASE_PASSWORD",
			Value:  "123",
			Public: true,
		},
	}
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	err = a.setEnvsToApp(
		bind.SetEnvApp{
			Envs:          envs,
			PublicOnly:    false,
			ShouldRestart: false,
		}, nil)
	c.Assert(err, check.IsNil)
	newApp, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	expected := map[string]bind.EnvVar{
		"DATABASE_HOST": {
			Name:   "DATABASE_HOST",
			Value:  "remotehost",
			Public: true,
		},
		"DATABASE_PASSWORD": {
			Name:   "DATABASE_PASSWORD",
			Value:  "123",
			Public: true,
		},
	}
	c.Assert(newApp.Env, check.DeepEquals, expected)
	c.Assert(s.provisioner.Restarts(&a, ""), check.Equals, 0)
}

func (s *S) TestSetEnvsWhenAppHaveNoUnits(c *check.C) {
	a := App{
		Name: "myapp",
		Env: map[string]bind.EnvVar{
			"DATABASE_HOST": {
				Name:   "DATABASE_HOST",
				Value:  "localhost",
				Public: false,
			},
		},
	}
	s.provisioner.PrepareOutput([]byte("exported"))
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	envs := []bind.EnvVar{
		{
			Name:   "DATABASE_HOST",
			Value:  "remotehost",
			Public: true,
		},
		{
			Name:   "DATABASE_PASSWORD",
			Value:  "123",
			Public: true,
		},
	}
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	err = a.setEnvsToApp(
		bind.SetEnvApp{
			Envs:          envs,
			PublicOnly:    false,
			ShouldRestart: false,
		}, nil)
	c.Assert(err, check.IsNil)
	newApp, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	expected := map[string]bind.EnvVar{
		"DATABASE_HOST": {
			Name:   "DATABASE_HOST",
			Value:  "remotehost",
			Public: true,
		},
		"DATABASE_PASSWORD": {
			Name:   "DATABASE_PASSWORD",
			Value:  "123",
			Public: true,
		},
	}
	c.Assert(newApp.Env, check.DeepEquals, expected)
	c.Assert(s.provisioner.Restarts(&a, ""), check.Equals, 0)
}

func (s *S) TestUnsetEnvRespectsThePublicOnlyFlagKeepPrivateVariablesWhenItsTrue(c *check.C) {
	a := App{
		Name: "myapp",
		Env: map[string]bind.EnvVar{
			"DATABASE_HOST": {
				Name:   "DATABASE_HOST",
				Value:  "localhost",
				Public: false,
			},
			"DATABASE_PASSWORD": {
				Name:   "DATABASE_PASSWORD",
				Value:  "123",
				Public: true,
			},
		},
		Quota: quota.Quota{
			Limit: 10,
		},
	}
	s.provisioner.PrepareOutput([]byte("exported"))
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	err = a.AddUnits(1, "web", nil)
	c.Assert(err, check.IsNil)
	err = a.UnsetEnvs(
		bind.UnsetEnvApp{
			VariableNames: []string{"DATABASE_HOST", "DATABASE_PASSWORD"},
			PublicOnly:    true,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	newApp, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	expected := map[string]bind.EnvVar{
		"DATABASE_HOST": {
			Name:   "DATABASE_HOST",
			Value:  "localhost",
			Public: false,
		},
	}
	c.Assert(newApp.Env, check.DeepEquals, expected)
	c.Assert(s.provisioner.Restarts(&a, ""), check.Equals, 1)
}

func (s *S) TestUnsetEnvRespectsThePublicOnlyFlagUnsettingAllVariablesWhenItsFalse(c *check.C) {
	a := App{
		Name: "myapp",
		Env: map[string]bind.EnvVar{
			"DATABASE_HOST": {
				Name:   "DATABASE_HOST",
				Value:  "localhost",
				Public: false,
			},
			"DATABASE_PASSWORD": {
				Name:   "DATABASE_PASSWORD",
				Value:  "123",
				Public: true,
			},
		},
		Quota: quota.Quota{
			Limit: 10,
		},
	}
	s.provisioner.PrepareOutput([]byte("exported"))
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	err = a.AddUnits(1, "web", nil)
	c.Assert(err, check.IsNil)
	err = a.UnsetEnvs(
		bind.UnsetEnvApp{
			VariableNames: []string{"DATABASE_HOST", "DATABASE_PASSWORD"},
			PublicOnly:    false,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	newApp, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(newApp.Env, check.DeepEquals, map[string]bind.EnvVar{})
	c.Assert(s.provisioner.Restarts(&a, ""), check.Equals, 1)
}

func (s *S) TestUnsetEnvWithNoRestartFlag(c *check.C) {
	a := App{
		Name: "myapp",
		Env: map[string]bind.EnvVar{
			"DATABASE_HOST": {
				Name:   "DATABASE_HOST",
				Value:  "localhost",
				Public: false,
			},
			"DATABASE_PASSWORD": {
				Name:   "DATABASE_PASSWORD",
				Value:  "123",
				Public: true,
			},
		},
		Quota: quota.Quota{
			Limit: 10,
		},
	}
	s.provisioner.PrepareOutput([]byte("exported"))
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	err = a.AddUnits(1, "web", nil)
	c.Assert(err, check.IsNil)
	err = a.UnsetEnvs(
		bind.UnsetEnvApp{
			VariableNames: []string{"DATABASE_HOST", "DATABASE_PASSWORD"},
			PublicOnly:    false,
			ShouldRestart: false,
		}, nil)
	c.Assert(err, check.IsNil)
	newApp, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(newApp.Env, check.DeepEquals, map[string]bind.EnvVar{})
	c.Assert(s.provisioner.Restarts(&a, ""), check.Equals, 0)
}
func (s *S) TestUnsetEnvNoUnits(c *check.C) {
	a := App{
		Name: "myapp",
		Env: map[string]bind.EnvVar{
			"DATABASE_HOST": {
				Name:   "DATABASE_HOST",
				Value:  "localhost",
				Public: false,
			},
			"DATABASE_PASSWORD": {
				Name:   "DATABASE_PASSWORD",
				Value:  "123",
				Public: true,
			},
		},
	}
	s.provisioner.PrepareOutput([]byte("exported"))
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	err = a.UnsetEnvs(
		bind.UnsetEnvApp{
			VariableNames: []string{"DATABASE_HOST", "DATABASE_PASSWORD"},
			PublicOnly:    false,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	newApp, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(newApp.Env, check.DeepEquals, map[string]bind.EnvVar{})
	c.Assert(s.provisioner.Restarts(&a, ""), check.Equals, 0)
}

func (s *S) TestGetEnvironmentVariableFromApp(c *check.C) {
	a := App{Name: "whole-lotta-love"}
	a.setEnv(bind.EnvVar{Name: "PATH", Value: "/"})
	v, err := a.getEnv("PATH")
	c.Assert(err, check.IsNil)
	c.Assert(v.Value, check.Equals, "/")
}

func (s *S) TestGetEnvReturnsErrorIfTheVariableIsNotDeclared(c *check.C) {
	a := App{Name: "what-is-and-what-should-never"}
	a.Env = make(map[string]bind.EnvVar)
	_, err := a.getEnv("PATH")
	c.Assert(err, check.NotNil)
}

func (s *S) TestGetEnvReturnsErrorIfTheEnvironmentMapIsNil(c *check.C) {
	a := App{Name: "what-is-and-what-should-never"}
	_, err := a.getEnv("PATH")
	c.Assert(err, check.NotNil)
}

func (s *S) TestInstanceEnvironmentReturnEnvironmentVariablesForTheServer(c *check.C) {
	envs := map[string]bind.EnvVar{
		"DATABASE_HOST": {Name: "DATABASE_HOST", Value: "localhost", Public: false, InstanceName: "mysql"},
		"DATABASE_USER": {Name: "DATABASE_USER", Value: "root", Public: true, InstanceName: "mysql"},
		"HOST":          {Name: "HOST", Value: "10.0.2.1", Public: false, InstanceName: "redis"},
	}
	expected := map[string]bind.EnvVar{
		"DATABASE_HOST": {Name: "DATABASE_HOST", Value: "localhost", Public: false, InstanceName: "mysql"},
		"DATABASE_USER": {Name: "DATABASE_USER", Value: "root", Public: true, InstanceName: "mysql"},
	}
	a := App{Name: "hi-there", Env: envs}
	c.Assert(a.InstanceEnv("mysql"), check.DeepEquals, expected)
}

func (s *S) TestInstanceEnvironmentDoesNotPanicIfTheEnvMapIsNil(c *check.C) {
	a := App{Name: "hi-there"}
	c.Assert(a.InstanceEnv("mysql"), check.DeepEquals, map[string]bind.EnvVar{})
}

func (s *S) TestAddCName(c *check.C) {
	app := &App{Name: "ktulu"}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	s.provisioner.Provision(app)
	defer s.provisioner.Destroy(app)
	err = app.AddCName("ktulu.mycompany.com")
	c.Assert(err, check.IsNil)
	app, err = GetByName(app.Name)
	c.Assert(err, check.IsNil)
	c.Assert(app.CName, check.DeepEquals, []string{"ktulu.mycompany.com"})
	err = app.AddCName("ktulu2.mycompany.com")
	c.Assert(err, check.IsNil)
	app, err = GetByName(app.Name)
	c.Assert(err, check.IsNil)
	c.Assert(app.CName, check.DeepEquals, []string{"ktulu.mycompany.com", "ktulu2.mycompany.com"})
}

func (s *S) TestAddCNameCantBeDuplicated(c *check.C) {
	app := &App{Name: "ktulu"}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	s.provisioner.Provision(app)
	defer s.provisioner.Destroy(app)
	err = app.AddCName("ktulu.mycompany.com")
	c.Assert(err, check.IsNil)
	err = app.AddCName("ktulu.mycompany.com")
	c.Assert(err, check.NotNil)
	c.Assert(err.Error(), check.Equals, "cname already exists!")
	app2 := &App{Name: "ktulu2"}
	err = s.conn.Apps().Insert(app2)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app2.Name})
	err = app2.AddCName("ktulu.mycompany.com")
	c.Assert(err, check.NotNil)
	c.Assert(err.Error(), check.Equals, "cname already exists!")
}

func (s *S) TestAddCNameWithWildCard(c *check.C) {
	app := &App{Name: "ktulu"}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	s.provisioner.Provision(app)
	defer s.provisioner.Destroy(app)
	err = app.AddCName("*.mycompany.com")
	c.Assert(err, check.IsNil)
	app, err = GetByName(app.Name)
	c.Assert(err, check.IsNil)
	c.Assert(app.CName, check.DeepEquals, []string{"*.mycompany.com"})
}

func (s *S) TestAddCNameErrsOnInvalid(c *check.C) {
	app := &App{Name: "ktulu"}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	s.provisioner.Provision(app)
	defer s.provisioner.Destroy(app)
	err = app.AddCName("_ktulu.mycompany.com")
	c.Assert(err, check.NotNil)
	c.Assert(err.Error(), check.Equals, "Invalid cname")
}

func (s *S) TestAddCNamePartialUpdate(c *check.C) {
	a := &App{Name: "master", Platform: "puppet"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	other := App{Name: a.Name}
	err = other.AddCName("ktulu.mycompany.com")
	c.Assert(err, check.IsNil)
	a, err = GetByName(a.Name)
	c.Assert(a.Platform, check.Equals, "puppet")
	c.Assert(a.Name, check.Equals, "master")
	c.Assert(a.CName, check.DeepEquals, []string{"ktulu.mycompany.com"})
}

func (s *S) TestAddCNameUnknownApp(c *check.C) {
	a := App{Name: "ktulu"}
	err := a.AddCName("ktulu.mycompany.com")
	c.Assert(err, check.NotNil)
}

func (s *S) TestAddCNameValidatesTheCName(c *check.C) {
	var data = []struct {
		input string
		valid bool
	}{
		{"ktulu.mycompany.com", true},
		{"ktulu-super.mycompany.com", true},
		{"ktulu_super.mycompany.com", true},
		{"KTULU.MYCOMPANY.COM", true},
		{"ktulu", true},
		{"KTULU", true},
		{"http://ktulu.mycompany.com", false},
		{"http:ktulu.mycompany.com", false},
		{"/ktulu.mycompany.com", false},
		{".ktulu.mycompany.com", false},
		{"0800.com", true},
		{"-0800.com", false},
		{"", true},
	}
	a := App{Name: "live-to-die"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	for _, t := range data {
		err := a.AddCName(t.input)
		if !t.valid {
			c.Check(err.Error(), check.Equals, "Invalid cname")
		} else {
			c.Check(err, check.IsNil)
		}
	}
}

func (s *S) TestAddCNameCallsProvisionerSetCName(c *check.C) {
	a := App{Name: "ktulu"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	err = a.AddCName("ktulu.mycompany.com", "ktulu2.mycompany.com")
	c.Assert(err, check.IsNil)
	hasCName := s.provisioner.HasCName(&a, "ktulu.mycompany.com")
	c.Assert(hasCName, check.Equals, true)
	hasCName = s.provisioner.HasCName(&a, "ktulu2.mycompany.com")
	c.Assert(hasCName, check.Equals, true)
}

func (s *S) TestRemoveCNameRemovesFromDatabase(c *check.C) {
	a := &App{Name: "ktulu"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	err = a.AddCName("ktulu.mycompany.com")
	c.Assert(err, check.IsNil)
	err = a.RemoveCName("ktulu.mycompany.com")
	c.Assert(err, check.IsNil)
	a, err = GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(a.CName, check.DeepEquals, []string{})
}

func (s *S) TestRemoveCNameWhichNoExists(c *check.C) {
	a := &App{Name: "ktulu"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	err = a.RemoveCName("ktulu.mycompany.com")
	c.Assert(err, check.NotNil)
	c.Assert(err.Error(), check.Equals, "cname not exists!")
}

func (s *S) TestRemoveMoreThanOneCName(c *check.C) {
	a := &App{Name: "ktulu"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	err = a.AddCName("ktulu.mycompany.com")
	c.Assert(err, check.IsNil)
	err = a.AddCName("ktulu2.mycompany.com")
	c.Assert(err, check.IsNil)
	err = a.RemoveCName("ktulu.mycompany.com", "ktulu2.mycompany.com")
	c.Assert(err, check.IsNil)
	a, err = GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(a.CName, check.DeepEquals, []string{})
}

func (s *S) TestRemoveCNameRemovesFromRouter(c *check.C) {
	a := App{Name: "ktulu"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	err = a.AddCName("ktulu.mycompany.com")
	c.Assert(err, check.IsNil)
	err = a.RemoveCName("ktulu.mycompany.com")
	c.Assert(err, check.IsNil)
	hasCName := s.provisioner.HasCName(&a, "ktulu.mycompany.com")
	c.Assert(hasCName, check.Equals, false)
}

func (s *S) TestAddInstanceFirst(c *check.C) {
	a := &App{Name: "dark"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	instance := bind.ServiceInstance{
		Name: "myinstance",
		Envs: map[string]string{
			"DATABASE_HOST": "localhost",
			"DATABASE_PORT": "3306",
			"DATABASE_USER": "root",
		},
	}
	err = a.AddInstance(
		bind.InstanceApp{
			ServiceName:   "myservice",
			Instance:      instance,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	a, err = GetByName(a.Name)
	c.Assert(err, check.IsNil)
	expected := map[string][]bind.ServiceInstance{"myservice": {instance}}
	env, ok := a.Env[TsuruServicesEnvVar]
	c.Assert(ok, check.Equals, true)
	c.Assert(env.Public, check.Equals, false)
	c.Assert(env.Name, check.Equals, TsuruServicesEnvVar)
	var got map[string][]bind.ServiceInstance
	err = json.Unmarshal([]byte(env.Value), &got)
	c.Assert(err, check.IsNil)
	c.Assert(got, check.DeepEquals, expected)
	delete(a.Env, TsuruServicesEnvVar)
	c.Assert(a.Env, check.DeepEquals, map[string]bind.EnvVar{
		"DATABASE_HOST": {
			Name:         "DATABASE_HOST",
			Value:        "localhost",
			Public:       false,
			InstanceName: "myinstance",
		},
		"DATABASE_PORT": {
			Name:         "DATABASE_PORT",
			Value:        "3306",
			Public:       false,
			InstanceName: "myinstance",
		},
		"DATABASE_USER": {
			Name:         "DATABASE_USER",
			Value:        "root",
			Public:       false,
			InstanceName: "myinstance",
		},
	})
	c.Assert(s.provisioner.Restarts(a, ""), check.Equals, 0)
}

func (s *S) TestAddInstanceWithUnits(c *check.C) {
	a := &App{Name: "dark", Quota: quota.Quota{Limit: 10}}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	err = a.AddUnits(1, "web", nil)
	c.Assert(err, check.IsNil)
	instance := bind.ServiceInstance{
		Name: "myinstance",
		Envs: map[string]string{
			"DATABASE_HOST": "localhost",
		},
	}
	err = a.AddInstance(
		bind.InstanceApp{
			ServiceName:   "myservice",
			Instance:      instance,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	a, err = GetByName(a.Name)
	c.Assert(err, check.IsNil)
	expected := map[string][]bind.ServiceInstance{"myservice": {instance}}
	env, ok := a.Env[TsuruServicesEnvVar]
	c.Assert(ok, check.Equals, true)
	c.Assert(env.Public, check.Equals, false)
	c.Assert(env.Name, check.Equals, TsuruServicesEnvVar)
	var got map[string][]bind.ServiceInstance
	err = json.Unmarshal([]byte(env.Value), &got)
	c.Assert(err, check.IsNil)
	c.Assert(got, check.DeepEquals, expected)
	delete(a.Env, TsuruServicesEnvVar)
	c.Assert(a.Env, check.DeepEquals, map[string]bind.EnvVar{
		"DATABASE_HOST": {
			Name:         "DATABASE_HOST",
			Value:        "localhost",
			Public:       false,
			InstanceName: "myinstance",
		},
	})
	c.Assert(s.provisioner.Restarts(a, ""), check.Equals, 1)
}

func (s *S) TestAddInstanceWithUnitsNoRestart(c *check.C) {
	a := &App{Name: "dark", Quota: quota.Quota{Limit: 10}}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	err = a.AddUnits(1, "web", nil)
	c.Assert(err, check.IsNil)
	instance := bind.ServiceInstance{
		Name: "myinstance",
		Envs: map[string]string{
			"DATABASE_HOST": "localhost",
		},
	}
	err = a.AddInstance(
		bind.InstanceApp{
			ServiceName:   "myservice",
			Instance:      instance,
			ShouldRestart: false,
		}, nil)
	c.Assert(err, check.IsNil)
	a, err = GetByName(a.Name)
	c.Assert(err, check.IsNil)
	expected := map[string][]bind.ServiceInstance{"myservice": {instance}}
	env, ok := a.Env[TsuruServicesEnvVar]
	c.Assert(ok, check.Equals, true)
	c.Assert(env.Public, check.Equals, false)
	c.Assert(env.Name, check.Equals, TsuruServicesEnvVar)
	var got map[string][]bind.ServiceInstance
	err = json.Unmarshal([]byte(env.Value), &got)
	c.Assert(err, check.IsNil)
	c.Assert(got, check.DeepEquals, expected)
	delete(a.Env, TsuruServicesEnvVar)
	c.Assert(a.Env, check.DeepEquals, map[string]bind.EnvVar{
		"DATABASE_HOST": {
			Name:         "DATABASE_HOST",
			Value:        "localhost",
			Public:       false,
			InstanceName: "myinstance",
		},
	})
	c.Assert(s.provisioner.Restarts(a, ""), check.Equals, 0)
}

func (s *S) TestAddInstanceMultipleServices(c *check.C) {
	a := &App{
		Name: "dark",
		Env: map[string]bind.EnvVar{
			TsuruServicesEnvVar: {
				Name:   TsuruServicesEnvVar,
				Public: false,
				Value:  `{"mysql": [{"instance_name": "mydb", "envs": {"DATABASE_NAME": "mydb"}}]}`,
			},
		},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	instance1 := bind.ServiceInstance{
		Name: "myinstance",
		Envs: map[string]string{"DATABASE_NAME": "myinstance"},
	}
	err = a.AddInstance(
		bind.InstanceApp{
			ServiceName:   "mysql",
			Instance:      instance1,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	instance2 := bind.ServiceInstance{
		Name: "yourinstance",
		Envs: map[string]string{"DATABASE_NAME": "supermongo"},
	}
	err = a.AddInstance(
		bind.InstanceApp{
			ServiceName:   "mongodb",
			Instance:      instance2,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	expected := map[string][]bind.ServiceInstance{
		"mysql":   {bind.ServiceInstance{Name: "mydb", Envs: map[string]string{"DATABASE_NAME": "mydb"}}, instance1},
		"mongodb": {instance2},
	}
	a, err = GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(a.parsedTsuruServices(), check.DeepEquals, expected)
	delete(a.Env, "TSURU_SERVICES")
	c.Assert(a.Env, check.DeepEquals, map[string]bind.EnvVar{
		"DATABASE_NAME": {
			Name:         "DATABASE_NAME",
			Value:        "supermongo",
			Public:       false,
			InstanceName: "yourinstance",
		},
	})
}

func (s *S) TestAddInstanceAndRemoveInstanceMultipleServices(c *check.C) {
	a := &App{
		Name: "fuchsia",
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	instance1 := bind.ServiceInstance{
		Name: "myinstance",
		Envs: map[string]string{"DATABASE_NAME": "myinstance"},
	}
	err = a.AddInstance(
		bind.InstanceApp{
			ServiceName:   "mysql",
			Instance:      instance1,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	instance2 := bind.ServiceInstance{
		Name: "yourinstance",
		Envs: map[string]string{"DATABASE_NAME": "supermongo"},
	}
	err = a.AddInstance(
		bind.InstanceApp{
			ServiceName:   "mongodb",
			Instance:      instance2,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	err = a.RemoveInstance(
		bind.InstanceApp{
			ServiceName:   "mysql",
			Instance:      instance1,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	expected := map[string][]bind.ServiceInstance{
		"mysql":   {},
		"mongodb": {instance2},
	}
	a, err = GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(a.parsedTsuruServices(), check.DeepEquals, expected)
	delete(a.Env, "TSURU_SERVICES")
	c.Assert(a.Env, check.DeepEquals, map[string]bind.EnvVar{
		"DATABASE_NAME": {
			Name:         "DATABASE_NAME",
			Value:        "supermongo",
			Public:       false,
			InstanceName: "yourinstance",
		},
	})
}

func (s *S) TestRemoveInstance(c *check.C) {
	a := &App{
		Name: "dark",
		Env: map[string]bind.EnvVar{
			TsuruServicesEnvVar: {
				Name:   TsuruServicesEnvVar,
				Public: false,
				Value:  `{"mysql": [{"instance_name": "mydb", "envs": {"DATABASE_NAME": "mydb"}}]}`,
			},
			"DATABASE_NAME": {
				Name:         "DATABASE_NAME",
				Public:       false,
				Value:        "mydb",
				InstanceName: "mydb",
			},
		},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	instance := bind.ServiceInstance{Name: "mydb", Envs: map[string]string{"DATABASE_NAME": "mydb"}}
	err = a.RemoveInstance(
		bind.InstanceApp{
			ServiceName:   "mysql",
			Instance:      instance,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	a, err = GetByName(a.Name)
	c.Assert(err, check.IsNil)
	env, ok := a.Env[TsuruServicesEnvVar]
	c.Assert(ok, check.Equals, true)
	c.Assert(env.Value, check.Equals, `{"mysql":[]}`)
	c.Assert(env.Public, check.Equals, false)
	c.Assert(env.Name, check.Equals, TsuruServicesEnvVar)
	delete(a.Env, TsuruServicesEnvVar)
	c.Assert(a.Env, check.DeepEquals, map[string]bind.EnvVar{})
	c.Assert(s.provisioner.Restarts(a, ""), check.Equals, 0)
}

func (s *S) TestRemoveInstanceShifts(c *check.C) {
	value := `{"mysql": [{"instance_name": "mydb", "envs": {"DATABASE_NAME": "mydb"}},
{"instance_name": "yourdb", "envs": {"DATABASE_NAME": "yourdb"}},
{"instance_name": "hisdb", "envs": {"DATABASE_NAME": "hisdb"}},
{"instance_name": "herdb", "envs": {"DATABASE_NAME": "herdb"}},
{"instance_name": "ourdb", "envs": {"DATABASE_NAME": "ourdb"}}
]}`
	a := &App{
		Name: "dark",
		Env: map[string]bind.EnvVar{
			TsuruServicesEnvVar: {
				Name:   TsuruServicesEnvVar,
				Public: false,
				Value:  value,
			},
		},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	instance := bind.ServiceInstance{Name: "hisdb"}
	err = a.RemoveInstance(
		bind.InstanceApp{
			ServiceName:   "mysql",
			Instance:      instance,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	expected := map[string][]bind.ServiceInstance{
		"mysql": {
			bind.ServiceInstance{Name: "mydb", Envs: map[string]string{"DATABASE_NAME": "mydb"}},
			bind.ServiceInstance{Name: "yourdb", Envs: map[string]string{"DATABASE_NAME": "yourdb"}},
			bind.ServiceInstance{Name: "herdb", Envs: map[string]string{"DATABASE_NAME": "herdb"}},
			bind.ServiceInstance{Name: "ourdb", Envs: map[string]string{"DATABASE_NAME": "ourdb"}},
		},
	}
	a, err = GetByName(a.Name)
	c.Assert(err, check.IsNil)
	env, ok := a.Env[TsuruServicesEnvVar]
	c.Assert(ok, check.Equals, true)
	c.Assert(env.Public, check.Equals, false)
	c.Assert(env.Name, check.Equals, TsuruServicesEnvVar)
	var got map[string][]bind.ServiceInstance
	err = json.Unmarshal([]byte(env.Value), &got)
	c.Assert(err, check.IsNil)
	c.Assert(got, check.DeepEquals, expected)
}

func (s *S) TestRemoveInstanceNotFound(c *check.C) {
	a := &App{
		Name: "dark",
		Env: map[string]bind.EnvVar{
			TsuruServicesEnvVar: {
				Name:   TsuruServicesEnvVar,
				Public: false,
				Value:  `{"mysql": [{"instance_name": "mydb", "envs": {"DATABASE_NAME": "mydb"}}]}`,
			},
		},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	instance := bind.ServiceInstance{Name: "yourdb"}
	err = a.RemoveInstance(
		bind.InstanceApp{
			ServiceName:   "mysql",
			Instance:      instance,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	a, err = GetByName(a.Name)
	c.Assert(err, check.IsNil)
	services := a.parsedTsuruServices()
	c.Assert(services, check.DeepEquals, map[string][]bind.ServiceInstance{
		"mysql": {
			{
				Name: "mydb",
				Envs: map[string]string{"DATABASE_NAME": "mydb"},
			},
		},
	})
}

func (s *S) TestRemoveInstanceServiceNotFound(c *check.C) {
	a := &App{
		Name: "dark",
		Env: map[string]bind.EnvVar{
			TsuruServicesEnvVar: {
				Name:   TsuruServicesEnvVar,
				Public: false,
				Value:  `{"mysql": [{"instance_name": "mydb", "envs": {"DATABASE_NAME": "mydb"}}]}`,
			},
		},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	instance := bind.ServiceInstance{Name: "mydb"}
	err = a.RemoveInstance(
		bind.InstanceApp{
			ServiceName:   "mongodb",
			Instance:      instance,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	a, err = GetByName(a.Name)
	c.Assert(err, check.IsNil)
	services := a.parsedTsuruServices()
	c.Assert(services, check.DeepEquals, map[string][]bind.ServiceInstance{
		"mysql": {
			{
				Name: "mydb",
				Envs: map[string]string{"DATABASE_NAME": "mydb"},
			},
		},
	})
}

func (s *S) TestRemoveInstanceWithUnits(c *check.C) {
	a := &App{
		Name: "dark",
		Env: map[string]bind.EnvVar{
			TsuruServicesEnvVar: {
				Name:   TsuruServicesEnvVar,
				Public: false,
				Value:  `{"mysql": [{"instance_name": "mydb", "envs": {"DATABASE_NAME": "mydb"}}]}`,
			},
			"DATABASE_NAME": {
				Name:         "DATABASE_NAME",
				Public:       false,
				Value:        "mydb",
				InstanceName: "mydb",
			},
		},
		Quota: quota.Quota{Limit: 10},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	err = a.AddUnits(1, "web", nil)
	c.Assert(err, check.IsNil)
	instance := bind.ServiceInstance{Name: "mydb", Envs: map[string]string{"DATABASE_NAME": "mydb"}}
	err = a.RemoveInstance(
		bind.InstanceApp{
			ServiceName:   "mysql",
			Instance:      instance,
			ShouldRestart: true,
		}, nil)
	c.Assert(err, check.IsNil)
	a, err = GetByName(a.Name)
	c.Assert(err, check.IsNil)
	env, ok := a.Env[TsuruServicesEnvVar]
	c.Assert(ok, check.Equals, true)
	c.Assert(env.Value, check.Equals, `{"mysql":[]}`)
	c.Assert(env.Public, check.Equals, false)
	c.Assert(env.Name, check.Equals, TsuruServicesEnvVar)
	delete(a.Env, TsuruServicesEnvVar)
	c.Assert(a.Env, check.DeepEquals, map[string]bind.EnvVar{})
	c.Assert(s.provisioner.Restarts(a, ""), check.Equals, 1)
}

func (s *S) TestRemoveInstanceWithUnitsNoRestart(c *check.C) {
	a := &App{
		Name: "dark",
		Env: map[string]bind.EnvVar{
			TsuruServicesEnvVar: {
				Name:   TsuruServicesEnvVar,
				Public: false,
				Value:  `{"mysql": [{"instance_name": "mydb", "envs": {"DATABASE_NAME": "mydb"}}]}`,
			},
			"DATABASE_NAME": {
				Name:         "DATABASE_NAME",
				Public:       false,
				Value:        "mydb",
				InstanceName: "mydb",
			},
		},
		Quota: quota.Quota{Limit: 10},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(a)
	defer s.provisioner.Destroy(a)
	err = a.AddUnits(1, "web", nil)
	c.Assert(err, check.IsNil)
	instance := bind.ServiceInstance{Name: "mydb", Envs: map[string]string{"DATABASE_NAME": "mydb"}}
	err = a.RemoveInstance(
		bind.InstanceApp{
			ServiceName:   "mysql",
			Instance:      instance,
			ShouldRestart: false,
		}, nil)
	c.Assert(err, check.IsNil)
	a, err = GetByName(a.Name)
	c.Assert(err, check.IsNil)
	env, ok := a.Env[TsuruServicesEnvVar]
	c.Assert(ok, check.Equals, true)
	c.Assert(env.Value, check.Equals, `{"mysql":[]}`)
	c.Assert(env.Public, check.Equals, false)
	c.Assert(env.Name, check.Equals, TsuruServicesEnvVar)
	delete(a.Env, TsuruServicesEnvVar)
	c.Assert(a.Env, check.DeepEquals, map[string]bind.EnvVar{})
	c.Assert(s.provisioner.Restarts(a, ""), check.Equals, 0)
}

func (s *S) TestIsValid(c *check.C) {
	errMsg := "Invalid app name, your app should have at most 63 characters, containing only lower case letters, numbers or dashes, starting with a letter."
	var data = []struct {
		name     string
		expected string
	}{
		{"myappmyappmyappmyappmyappmyappmyappmyappmyappmyappmyappmyappmyapp", errMsg},
		{"myappmyappmyappmyappmyappmyappmyappmyappmyappmyappmyappmyappmyap", errMsg},
		{"myappmyappmyappmyappmyappmyappmyappmyappmyappmyappmyappmyappmya", ""},
		{"myApp", errMsg},
		{"my app", errMsg},
		{"123myapp", errMsg},
		{"myapp", ""},
		{"_theirapp", errMsg},
		{"my-app", ""},
		{"-myapp", errMsg},
		{"my_app", errMsg},
		{"b", ""},
		{InternalAppName, errMsg},
	}
	for _, d := range data {
		a := App{Name: d.name}
		if valid := a.validate(); valid != nil && valid.Error() != d.expected {
			c.Errorf("Is %q a valid app name? Expected: %v. Got: %v.", d.name, d.expected, valid)
		}
	}
}

func (s *S) TestRestart(c *check.C) {
	s.provisioner.PrepareOutput([]byte("not yaml")) // loadConf
	a := App{
		Name:     "someApp",
		Platform: "django",
		Teams:    []string{s.team.Name},
	}
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	var b bytes.Buffer
	err := a.Restart("", &b)
	c.Assert(err, check.IsNil)
	c.Assert(b.String(), check.Matches, `(?s).*---- Restarting the app "someApp" ----.*`)
	restarts := s.provisioner.Restarts(&a, "")
	c.Assert(restarts, check.Equals, 1)
}

func (s *S) TestStop(c *check.C) {
	a := App{Name: "app"}
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	var buf bytes.Buffer
	err = a.Stop(&buf, "")
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Find(bson.M{"name": a.GetName()}).One(&a)
	c.Assert(err, check.IsNil)
	units, err := a.Units()
	c.Assert(err, check.IsNil)
	for _, u := range units {
		c.Assert(u.Status, check.Equals, provision.StatusStopped)
	}
}

func (s *S) TestLog(c *check.C) {
	a := App{Name: "newApp"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.logConn.Logs(a.Name).DropCollection()
	}()
	err = a.Log("last log msg", "tsuru", "outermachine")
	c.Assert(err, check.IsNil)
	var logs []Applog
	err = s.logConn.Logs(a.Name).Find(nil).All(&logs)
	c.Assert(err, check.IsNil)
	c.Assert(logs, check.HasLen, 1)
	c.Assert(logs[0].Message, check.Equals, "last log msg")
	c.Assert(logs[0].Source, check.Equals, "tsuru")
	c.Assert(logs[0].AppName, check.Equals, a.Name)
	c.Assert(logs[0].Unit, check.Equals, "outermachine")
}

func (s *S) TestLogShouldAddOneRecordByLine(c *check.C) {
	a := App{Name: "newApp"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.logConn.Logs(a.Name).DropCollection()
	}()
	err = a.Log("last log msg\nfirst log", "source", "machine")
	c.Assert(err, check.IsNil)
	var logs []Applog
	err = s.logConn.Logs(a.Name).Find(nil).Sort("$natural").All(&logs)
	c.Assert(err, check.IsNil)
	c.Assert(logs, check.HasLen, 2)
	c.Assert(logs[0].Message, check.Equals, "last log msg")
	c.Assert(logs[1].Message, check.Equals, "first log")
}

func (s *S) TestLogShouldNotLogBlankLines(c *check.C) {
	a := App{Name: "ich"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	err = a.Log("some message", "tsuru", "machine")
	c.Assert(err, check.IsNil)
	err = a.Log("", "", "")
	c.Assert(err, check.IsNil)
	count, err := s.logConn.Logs(a.Name).Find(nil).Count()
	c.Assert(err, check.IsNil)
	c.Assert(count, check.Equals, 1)
}

func (s *S) TestLogWithListeners(c *check.C) {
	var logs struct {
		l []Applog
		sync.Mutex
	}
	a := App{
		Name: "newApp",
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	l, err := NewLogListener(&a, Applog{})
	c.Assert(err, check.IsNil)
	defer l.Close()
	go func() {
		for log := range l.C {
			logs.Lock()
			logs.l = append(logs.l, log)
			logs.Unlock()
		}
	}()
	err = a.Log("last log msg", "tsuru", "machine")
	c.Assert(err, check.IsNil)
	defer s.logConn.Logs(a.Name).DropCollection()
	done := make(chan bool, 1)
	q := make(chan bool)
	go func(quit chan bool) {
		for range time.Tick(1e3) {
			select {
			case <-quit:
				return
			default:
			}
			logs.Lock()
			if len(logs.l) == 1 {
				logs.Unlock()
				done <- true
				return
			}
			logs.Unlock()
		}
	}(q)
	select {
	case <-done:
	case <-time.After(2e9):
		defer close(q)
		c.Fatal("Timed out.")
	}
	logs.Lock()
	c.Assert(logs.l, check.HasLen, 1)
	log := logs.l[0]
	logs.Unlock()
	c.Assert(log.Message, check.Equals, "last log msg")
	c.Assert(log.Source, check.Equals, "tsuru")
	c.Assert(log.Unit, check.Equals, "machine")
}

func (s *S) TestLastLogs(c *check.C) {
	app := App{
		Name:     "app3",
		Platform: "vougan",
		Teams:    []string{s.team.Name},
	}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	for i := 0; i < 15; i++ {
		app.Log(strconv.Itoa(i), "tsuru", "rdaneel")
		time.Sleep(1e6) // let the time flow
	}
	app.Log("app3 log from circus", "circus", "rdaneel")
	logs, err := app.LastLogs(10, Applog{Source: "tsuru"})
	c.Assert(err, check.IsNil)
	c.Assert(logs, check.HasLen, 10)
	for i := 5; i < 15; i++ {
		c.Check(logs[i-5].Message, check.Equals, strconv.Itoa(i))
		c.Check(logs[i-5].Source, check.Equals, "tsuru")
	}
}

func (s *S) TestLastLogsUnitFilter(c *check.C) {
	app := App{
		Name:     "app3",
		Platform: "vougan",
		Teams:    []string{s.team.Name},
	}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	for i := 0; i < 15; i++ {
		app.Log(strconv.Itoa(i), "tsuru", "rdaneel")
		time.Sleep(1e6) // let the time flow
	}
	app.Log("app3 log from circus", "circus", "rdaneel")
	app.Log("app3 log from tsuru", "tsuru", "seldon")
	logs, err := app.LastLogs(10, Applog{Source: "tsuru", Unit: "rdaneel"})
	c.Assert(err, check.IsNil)
	c.Assert(logs, check.HasLen, 10)
	for i := 5; i < 15; i++ {
		c.Check(logs[i-5].Message, check.Equals, strconv.Itoa(i))
		c.Check(logs[i-5].Source, check.Equals, "tsuru")
	}
}

func (s *S) TestLastLogsEmpty(c *check.C) {
	app := App{
		Name:     "app33",
		Platform: "vougan",
		Teams:    []string{s.team.Name},
	}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	logs, err := app.LastLogs(10, Applog{Source: "tsuru"})
	c.Assert(err, check.IsNil)
	c.Assert(logs, check.DeepEquals, []Applog{})
}

func (s *S) TestGetTeams(c *check.C) {
	app := App{Name: "app", Teams: []string{s.team.Name}}
	teams := app.GetTeams()
	c.Assert(teams, check.HasLen, 1)
	c.Assert(teams[0].Name, check.Equals, s.team.Name)
}

func (s *S) TestGetUnits(c *check.C) {
	app := App{Name: "app"}
	s.provisioner.Provision(&app)
	defer s.provisioner.Destroy(&app)
	s.provisioner.AddUnits(&app, 1, "web", nil)
	bindUnits, err := app.GetUnits()
	c.Assert(err, check.IsNil)
	c.Assert(bindUnits, check.HasLen, 1)
	units, err := app.Units()
	c.Assert(err, check.IsNil)
	c.Assert(units[0].Ip, check.Equals, bindUnits[0].GetIp())
}

func (s *S) TestAppMarshalJSON(c *check.C) {
	repository.Manager().CreateRepository("name", nil)
	app := App{
		Name:      "name",
		Platform:  "Framework",
		Teams:     []string{"team1"},
		Ip:        "10.10.10.1",
		CName:     []string{"name.mycompany.com"},
		Owner:     "appOwner",
		Deploys:   7,
		Pool:      "test",
		Plan:      Plan{Name: "myplan", Memory: 64, Swap: 128, CpuShare: 100},
		TeamOwner: "myteam",
	}
	expected := map[string]interface{}{
		"name":       "name",
		"platform":   "Framework",
		"repository": "git@" + repositorytest.ServerHost + ":name.git",
		"teams":      []interface{}{"team1"},
		"units":      nil,
		"ip":         "10.10.10.1",
		"cname":      []interface{}{"name.mycompany.com"},
		"owner":      "appOwner",
		"deploys":    float64(7),
		"pool":       "test",
		"teamowner":  "myteam",
		"lock":       s.zeroLock,
		"plan": map[string]interface{}{
			"name":     "myplan",
			"memory":   float64(64),
			"swap":     float64(128),
			"cpushare": float64(100),
		},
	}
	data, err := app.MarshalJSON()
	c.Assert(err, check.IsNil)
	result := make(map[string]interface{})
	err = json.Unmarshal(data, &result)
	c.Assert(err, check.IsNil)
	c.Assert(result, check.DeepEquals, expected)
}

func (s *S) TestAppMarshalJSONWithoutRepository(c *check.C) {
	app := App{
		Name:      "name",
		Platform:  "Framework",
		Teams:     []string{"team1"},
		Ip:        "10.10.10.1",
		CName:     []string{"name.mycompany.com"},
		Owner:     "appOwner",
		Deploys:   7,
		Pool:      "pool1",
		Plan:      Plan{Name: "myplan", Memory: 64, Swap: 128, CpuShare: 100},
		TeamOwner: "myteam",
	}
	expected := map[string]interface{}{
		"name":       "name",
		"platform":   "Framework",
		"repository": "",
		"teams":      []interface{}{"team1"},
		"units":      nil,
		"ip":         "10.10.10.1",
		"cname":      []interface{}{"name.mycompany.com"},
		"owner":      "appOwner",
		"deploys":    float64(7),
		"pool":       "pool1",
		"teamowner":  "myteam",
		"lock":       s.zeroLock,
		"plan": map[string]interface{}{
			"name":     "myplan",
			"memory":   float64(64),
			"swap":     float64(128),
			"cpushare": float64(100),
		},
	}
	data, err := app.MarshalJSON()
	c.Assert(err, check.IsNil)
	result := make(map[string]interface{})
	err = json.Unmarshal(data, &result)
	c.Assert(err, check.IsNil)
	c.Assert(result, check.DeepEquals, expected)
}

func (s *S) TestRun(c *check.C) {
	s.provisioner.PrepareOutput([]byte("a lot of files"))
	app := App{Name: "myapp"}
	s.provisioner.Provision(&app)
	defer s.provisioner.Destroy(&app)
	s.provisioner.AddUnits(&app, 1, "web", nil)
	var buf bytes.Buffer
	err := app.Run("ls -lh", &buf, false)
	c.Assert(err, check.IsNil)
	c.Assert(buf.String(), check.Equals, "a lot of files")
	expected := "[ -f /home/application/apprc ] && source /home/application/apprc;"
	expected += " [ -d /home/application/current ] && cd /home/application/current;"
	expected += " ls -lh"
	cmds := s.provisioner.GetCmds(expected, &app)
	c.Assert(cmds, check.HasLen, 1)
	var logs []Applog
	timeout := time.After(5 * time.Second)
	for {
		logs, err = app.LastLogs(10, Applog{})
		c.Assert(err, check.IsNil)
		if len(logs) > 1 {
			break
		}
		select {
		case <-timeout:
			c.Fatal("timeout waiting for logs")
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.Assert(err, check.IsNil)
	c.Assert(logs, check.HasLen, 2)
	c.Assert(logs[1].Message, check.Equals, "a lot of files")
	c.Assert(logs[1].Source, check.Equals, "app-run")
}

func (s *S) TestRunOnce(c *check.C) {
	s.provisioner.PrepareOutput([]byte("a lot of files"))
	app := App{
		Name: "myapp",
	}
	s.provisioner.Provision(&app)
	defer s.provisioner.Destroy(&app)
	s.provisioner.AddUnits(&app, 1, "web", nil)
	var buf bytes.Buffer
	err := app.Run("ls -lh", &buf, true)
	c.Assert(err, check.IsNil)
	c.Assert(buf.String(), check.Equals, "a lot of files")
	expected := "[ -f /home/application/apprc ] && source /home/application/apprc;"
	expected += " [ -d /home/application/current ] && cd /home/application/current;"
	expected += " ls -lh"
	cmds := s.provisioner.GetCmds(expected, &app)
	c.Assert(cmds, check.HasLen, 1)
}

func (s *S) TestRunWithoutEnv(c *check.C) {
	s.provisioner.PrepareOutput([]byte("a lot of files"))
	app := App{
		Name: "myapp",
	}
	s.provisioner.Provision(&app)
	defer s.provisioner.Destroy(&app)
	s.provisioner.AddUnits(&app, 1, "web", nil)
	var buf bytes.Buffer
	err := app.run("ls -lh", &buf, false)
	c.Assert(err, check.IsNil)
	c.Assert(buf.String(), check.Equals, "a lot of files")
	cmds := s.provisioner.GetCmds("ls -lh", &app)
	c.Assert(cmds, check.HasLen, 1)
}

func (s *S) TestEnvs(c *check.C) {
	app := App{
		Name: "time",
		Env: map[string]bind.EnvVar{
			"http_proxy": {
				Name:   "http_proxy",
				Value:  "http://theirproxy.com:3128/",
				Public: true,
			},
		},
	}
	env := app.Envs()
	c.Assert(env, check.DeepEquals, app.Env)
}

func (s *S) TestListReturnsAppsForAGivenUser(c *check.C) {
	a := App{
		Name:  "testapp",
		Teams: []string{s.team.Name},
	}
	a2 := App{
		Name:  "othertestapp",
		Teams: []string{"commonteam", s.team.Name},
	}
	err := s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a2)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.conn.Apps().Remove(bson.M{"name": a2.Name})
	}()
	apps, err := List(nil)
	c.Assert(err, check.IsNil)
	c.Assert(len(apps), check.Equals, 2)
}

func (s *S) TestListReturnsAppsForAGivenUserFilteringByName(c *check.C) {
	a := App{
		Name:  "app1",
		Teams: []string{s.team.Name},
	}
	a2 := App{
		Name:  "app2",
		Teams: []string{s.team.Name},
	}
	a3 := App{
		Name:  "foo",
		Teams: []string{s.team.Name},
	}
	err := s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a2)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a3)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.conn.Apps().Remove(bson.M{"name": a2.Name})
		s.conn.Apps().Remove(bson.M{"name": a3.Name})
	}()
	apps, err := List(&Filter{Name: "app\\d{1}"})
	c.Assert(err, check.IsNil)
	c.Assert(len(apps), check.Equals, 2)
}

func (s *S) TestListReturnsAppsForAGivenUserFilteringByPlatform(c *check.C) {
	a := App{
		Name:     "testapp",
		Teams:    []string{s.team.Name},
		Platform: "ruby",
	}
	a2 := App{
		Name:     "othertestapp",
		Teams:    []string{"commonteam", s.team.Name},
		Platform: "python",
	}
	err := s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a2)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.conn.Apps().Remove(bson.M{"name": a2.Name})
	}()
	apps, err := List(&Filter{Platform: "ruby"})
	c.Assert(err, check.IsNil)
	c.Assert(len(apps), check.Equals, 1)
}

func (s *S) TestListReturnsAppsForAGivenUserFilteringByTeamOwner(c *check.C) {
	a := App{
		Name:      "testapp",
		Teams:     []string{s.team.Name},
		TeamOwner: "foo",
	}
	a2 := App{
		Name:      "othertestapp",
		Teams:     []string{"commonteam", s.team.Name},
		TeamOwner: "bar",
	}
	err := s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a2)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.conn.Apps().Remove(bson.M{"name": a2.Name})
	}()
	apps, err := List(&Filter{TeamOwner: "foo"})
	c.Assert(err, check.IsNil)
	c.Assert(len(apps), check.Equals, 1)
}

func (s *S) TestListReturnsAppsForAGivenUserFilteringByOwner(c *check.C) {
	a := App{
		Name:  "testapp",
		Teams: []string{s.team.Name},
		Owner: "foo",
	}
	a2 := App{
		Name:  "othertestapp",
		Teams: []string{"commonteam", s.team.Name},
		Owner: "bar",
	}
	err := s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a2)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.conn.Apps().Remove(bson.M{"name": a2.Name})
	}()
	apps, err := List(&Filter{UserOwner: "foo"})
	c.Assert(err, check.IsNil)
	c.Assert(len(apps), check.Equals, 1)
}

func (s *S) TestListReturnsAppsForAGivenUserFilteringByLockState(c *check.C) {
	a := App{
		Name:  "testapp",
		Teams: []string{s.team.Name},
		Owner: "foo",
	}
	a2 := App{
		Name:  "othertestapp",
		Teams: []string{"commonteam", s.team.Name},
		Owner: "bar",
		Lock: AppLock{
			Locked:      true,
			Reason:      "something",
			Owner:       s.user.Email,
			AcquireDate: time.Now(),
		},
	}
	err := s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a2)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.conn.Apps().Remove(bson.M{"name": a2.Name})
	}()
	apps, err := List(&Filter{Locked: true})
	c.Assert(err, check.IsNil)
	c.Assert(len(apps), check.Equals, 1)
	c.Assert(apps[0].Name, check.Equals, "othertestapp")
}

func (s *S) TestListAll(c *check.C) {
	a := App{
		Name:  "testapp",
		Teams: []string{s.team.Name},
	}
	a2 := App{
		Name:  "othertestapp",
		Teams: []string{"commonteam", s.team.Name},
	}
	err := s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a2)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.conn.Apps().Remove(bson.M{"name": a2.Name})
	}()
	apps, err := List(nil)
	c.Assert(err, check.IsNil)
	c.Assert(len(apps), check.Equals, 2)
}

func (s *S) TestListFilteringByName(c *check.C) {
	a := App{
		Name:  "app1",
		Teams: []string{s.team.Name},
	}
	a2 := App{
		Name:  "app2",
		Teams: []string{s.team.Name},
	}
	a3 := App{
		Name:  "foo",
		Teams: []string{s.team.Name},
	}
	err := s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a2)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a3)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.conn.Apps().Remove(bson.M{"name": a2.Name})
		s.conn.Apps().Remove(bson.M{"name": a3.Name})
	}()
	apps, err := List(&Filter{Name: "app\\d{1}"})
	c.Assert(err, check.IsNil)
	c.Assert(len(apps), check.Equals, 2)
}

func (s *S) TestListFilteringByPlatform(c *check.C) {
	a := App{
		Name:     "testapp",
		Teams:    []string{s.team.Name},
		Platform: "ruby",
	}
	a2 := App{
		Name:     "othertestapp",
		Teams:    []string{s.team.Name},
		Platform: "python",
	}
	err := s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a2)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.conn.Apps().Remove(bson.M{"name": a2.Name})
	}()
	apps, err := List(&Filter{Platform: "ruby"})
	c.Assert(err, check.IsNil)
	c.Assert(len(apps), check.Equals, 1)
}

func (s *S) TestListFilteringByOwner(c *check.C) {
	a := App{
		Name:  "testapp",
		Teams: []string{s.team.Name},
		Owner: "foo",
	}
	a2 := App{
		Name:  "othertestapp",
		Teams: []string{s.team.Name},
		Owner: "bar",
	}
	err := s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a2)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.conn.Apps().Remove(bson.M{"name": a2.Name})
	}()
	apps, err := List(&Filter{UserOwner: "foo"})
	c.Assert(err, check.IsNil)
	c.Assert(len(apps), check.Equals, 1)
}

func (s *S) TestListFilteringByTeamOwner(c *check.C) {
	a := App{
		Name:      "testapp",
		Teams:     []string{s.team.Name},
		TeamOwner: "foo",
	}
	a2 := App{
		Name:      "othertestapp",
		Teams:     []string{s.team.Name},
		TeamOwner: "bar",
	}
	err := s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a2)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.conn.Apps().Remove(bson.M{"name": a2.Name})
	}()
	apps, err := List(&Filter{TeamOwner: "foo"})
	c.Assert(err, check.IsNil)
	c.Assert(len(apps), check.Equals, 1)
}

func (s *S) TestListFilteringByPool(c *check.C) {
	opts := provision.AddPoolOptions{Name: "test2", Default: false}
	err := provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	a := App{
		Name:  "testapp",
		Teams: []string{s.team.Name},
		Owner: "foo",
		Pool:  opts.Name,
	}
	a2 := App{
		Name:  "othertestapp",
		Teams: []string{s.team.Name},
		Owner: "bar",
		Pool:  s.Pool,
	}
	err = s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a2)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.conn.Apps().Remove(bson.M{"name": a2.Name})
	}()
	apps, err := List(&Filter{Pool: s.Pool})
	c.Assert(err, check.IsNil)
	c.Assert(len(apps), check.Equals, 1)
	c.Assert(apps[0].Name, check.Equals, a2.Name)
	c.Assert(apps[0].Pool, check.Equals, a2.Pool)
}

func (s *S) TestListReturnsEmptyAppArrayWhenUserHasNoAccessToAnyApp(c *check.C) {
	apps, err := List(nil)
	c.Assert(err, check.IsNil)
	c.Assert(apps, check.DeepEquals, []App(nil))
}

func (s *S) TestListReturnsAllAppsWhenUsedWithNoFilters(c *check.C) {
	a := App{Name: "testApp", Teams: []string{"notAdmin", "noSuperUser"}}
	err := s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	apps, err := List(nil)
	c.Assert(len(apps), Greater, 0)
	c.Assert(apps[0].Name, check.Equals, "testApp")
	c.Assert(apps[0].Teams, check.DeepEquals, []string{"notAdmin", "noSuperUser"})
}

func (s *S) TestListFilteringExtraWithOr(c *check.C) {
	opts := provision.AddPoolOptions{Name: "test2", Default: false}
	err := provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	a := App{
		Name:  "testapp1",
		Teams: []string{s.team.Name},
		Owner: "foo",
		Pool:  opts.Name,
	}
	a2 := App{
		Name:  "testapp2",
		Teams: []string{s.team.Name},
		Owner: "bar",
		Pool:  s.Pool,
	}
	a3 := App{
		Name:  "testapp3",
		Teams: []string{"otherteam"},
		Owner: "bar",
		Pool:  opts.Name,
	}
	err = s.conn.Apps().Insert(&a)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a2)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(&a3)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": a.Name})
		s.conn.Apps().Remove(bson.M{"name": a2.Name})
		s.conn.Apps().Remove(bson.M{"name": a3.Name})
	}()
	f := &Filter{}
	f.ExtraIn("pool", s.Pool)
	f.ExtraIn("teams", "otherteam")
	apps, err := List(f)
	c.Assert(err, check.IsNil)
	var appNames []string
	for _, a := range apps {
		appNames = append(appNames, a.Name)
	}
	sort.Strings(appNames)
	c.Assert(appNames, check.DeepEquals, []string{a2.Name, a3.Name})
}

func (s *S) TestGetName(c *check.C) {
	a := App{Name: "something"}
	c.Assert(a.GetName(), check.Equals, a.Name)
}

func (s *S) TestGetIP(c *check.C) {
	a := App{Ip: "10.10.10.10"}
	c.Assert(a.GetIp(), check.Equals, a.Ip)
}

func (s *S) TestGetQuota(c *check.C) {
	a := App{Quota: quota.Unlimited}
	c.Assert(a.GetQuota(), check.DeepEquals, quota.Unlimited)
}

func (s *S) TestSetQuotaInUse(c *check.C) {
	app := App{Name: "someapp", Quota: quota.Quota{Limit: 5, InUse: 5}}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	err = app.SetQuotaInUse(3)
	c.Assert(err, check.IsNil)
	a, err := GetByName(app.Name)
	c.Assert(err, check.IsNil)
	c.Assert(a.Quota, check.DeepEquals, quota.Quota{Limit: 5, InUse: 3})
}

func (s *S) TestSetQuotaInUseNotFound(c *check.C) {
	app := App{Name: "someapp", Quota: quota.Quota{Limit: 5, InUse: 5}}
	err := app.SetQuotaInUse(3)
	c.Assert(err, check.Equals, ErrAppNotFound)
}

func (s *S) TestSetQuotaInUseUnlimited(c *check.C) {
	app := App{Name: "someapp", Quota: quota.Unlimited}
	err := s.conn.Apps().Insert(app)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	err = app.SetQuotaInUse(3)
	c.Assert(err, check.IsNil)
	a, err := GetByName(app.Name)
	c.Assert(err, check.IsNil)
	c.Assert(a.Quota, check.DeepEquals, quota.Quota{Limit: -1, InUse: 3})

}

func (s *S) TestSetQuotaInUseInvalid(c *check.C) {
	app := App{Name: "someapp", Quota: quota.Quota{Limit: 5, InUse: 3}}
	err := app.SetQuotaInUse(6)
	c.Assert(err, check.NotNil)
	e, ok := err.(*quota.QuotaExceededError)
	c.Assert(ok, check.Equals, true)
	c.Assert(e.Available, check.Equals, uint(5))
	c.Assert(e.Requested, check.Equals, uint(6))
	err = app.SetQuotaInUse(-1)
	c.Assert(err, check.NotNil)
	c.Check(err.Error(), check.Equals, "invalid value, cannot be lesser than 0")
}

func (s *S) TestGetPlatform(c *check.C) {
	a := App{Platform: "django"}
	c.Assert(a.GetPlatform(), check.Equals, a.Platform)
}

func (s *S) TestGetDeploys(c *check.C) {
	a := App{Deploys: 3}
	c.Assert(a.GetDeploys(), check.Equals, a.Deploys)
}

func (s *S) TestGetMemory(c *check.C) {
	a := App{Plan: Plan{Memory: 10}}
	c.Assert(a.GetMemory(), check.Equals, a.Plan.Memory)
}

func (s *S) TestGetSwap(c *check.C) {
	a := App{Plan: Plan{Swap: 20}}
	c.Assert(a.GetSwap(), check.Equals, a.Plan.Swap)
}

func (s *S) TestAppUnits(c *check.C) {
	a := App{Name: "anycolor"}
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 1, "web", nil)
	units, err := a.Units()
	c.Assert(err, check.IsNil)
	c.Assert(units, check.HasLen, 1)
}

func (s *S) TestAppAvailable(c *check.C) {
	a := App{
		Name: "anycolor",
	}
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 1, "web", nil)
	c.Assert(a.Available(), check.Equals, true)
	s.provisioner.Stop(&a, "")
	c.Assert(a.Available(), check.Equals, false)
}

func (s *S) TestSwap(c *check.C) {
	var err error
	app1 := &App{Name: "app1", CName: []string{"cname"}}
	err = s.provisioner.Provision(app1)
	c.Assert(err, check.IsNil)
	app1.Ip, err = s.provisioner.Addr(app1)
	c.Assert(err, check.IsNil)
	oldIp1 := app1.Ip
	err = s.conn.Apps().Insert(app1)
	c.Assert(err, check.IsNil)
	app2 := &App{Name: "app2"}
	err = s.provisioner.Provision(app2)
	c.Assert(err, check.IsNil)
	app2.Ip, err = s.provisioner.Addr(app2)
	c.Assert(err, check.IsNil)
	oldIp2 := app2.Ip
	err = s.conn.Apps().Insert(app2)
	c.Assert(err, check.IsNil)
	defer func() {
		s.conn.Apps().Remove(bson.M{"name": app1.Name})
		s.conn.Apps().Remove(bson.M{"name": app2.Name})
	}()
	err = Swap(app1, app2)
	c.Assert(err, check.IsNil)
	c.Assert(app1.CName, check.IsNil)
	c.Assert(app2.CName, check.DeepEquals, []string{"cname"})
	c.Assert(app1.Ip, check.Equals, oldIp2)
	c.Assert(app2.Ip, check.Equals, oldIp1)
}

func (s *S) TestStart(c *check.C) {
	s.provisioner.PrepareOutput([]byte("not yaml")) // loadConf
	a := App{
		Name:     "someApp",
		Platform: "django",
		Teams:    []string{s.team.Name},
	}
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	var b bytes.Buffer
	err = a.Start(&b, "")
	c.Assert(err, check.IsNil)
	starts := s.provisioner.Starts(&a, "")
	c.Assert(starts, check.Equals, 1)
}

func (s *S) TestAppSetUpdatePlatform(c *check.C) {
	a := App{
		Name:     "someApp",
		Platform: "django",
		Teams:    []string{s.team.Name},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	a.SetUpdatePlatform(true)
	app, err := GetByName("someApp")
	c.Assert(err, check.IsNil)
	c.Assert(app.UpdatePlatform, check.Equals, true)
}

func (s *S) TestAppAcquireApplicationLock(c *check.C) {
	a := App{
		Name: "someApp",
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	locked, err := AcquireApplicationLock(a.Name, "foo", "/something")
	c.Assert(err, check.IsNil)
	c.Assert(locked, check.Equals, true)
	app, err := GetByName("someApp")
	c.Assert(err, check.IsNil)
	c.Assert(app.Lock.Locked, check.Equals, true)
	c.Assert(app.Lock.Owner, check.Equals, "foo")
	c.Assert(app.Lock.Reason, check.Equals, "/something")
	c.Assert(app.Lock.AcquireDate, check.NotNil)
}

func (s *S) TestAppAcquireApplicationLockNonExistentApp(c *check.C) {
	locked, err := AcquireApplicationLock("myApp", "foo", "/something")
	c.Assert(err, check.IsNil)
	c.Assert(locked, check.Equals, false)
}

func (s *S) TestAppAcquireApplicationLockAlreadyLocked(c *check.C) {
	a := App{
		Name: "someApp",
		Lock: AppLock{
			Locked:      true,
			Reason:      "/app/my-app/deploy",
			Owner:       "someone",
			AcquireDate: time.Date(2048, time.November, 10, 10, 0, 0, 0, time.UTC),
		},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	locked, err := AcquireApplicationLock(a.Name, "foo", "/something")
	c.Assert(err, check.IsNil)
	c.Assert(locked, check.Equals, false)
	app, err := GetByName("someApp")
	c.Assert(err, check.IsNil)
	c.Assert(app.Lock.Locked, check.Equals, true)
	c.Assert(app.Lock.Owner, check.Equals, "someone")
	c.Assert(app.Lock.Reason, check.Equals, "/app/my-app/deploy")
	c.Assert(app.Lock.AcquireDate, check.NotNil)
}

func (s *S) TestAppAcquireApplicationLockWait(c *check.C) {
	a := App{Name: "test-lock-app"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	locked, err := AcquireApplicationLock(a.Name, "foo", "/something")
	c.Assert(err, check.IsNil)
	c.Assert(locked, check.Equals, true)
	wg := sync.WaitGroup{}
	wg.Add(1)
	defer wg.Wait()
	go func() {
		defer wg.Done()
		time.Sleep(time.Second)
		ReleaseApplicationLock(a.Name)
	}()
	locked, err = AcquireApplicationLockWait(a.Name, "zzz", "/other", 10*time.Second)
	c.Assert(err, check.IsNil)
	c.Assert(locked, check.Equals, true)
	app, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(app.Lock.Locked, check.Equals, true)
	c.Assert(app.Lock.Owner, check.Equals, "zzz")
	c.Assert(app.Lock.Reason, check.Equals, "/other")
	c.Assert(app.Lock.AcquireDate, check.NotNil)
}

func (s *S) TestAppAcquireApplicationLockWaitWithoutRelease(c *check.C) {
	a := App{Name: "test-lock-app"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	locked, err := AcquireApplicationLock(a.Name, "foo", "/something")
	c.Assert(err, check.IsNil)
	c.Assert(locked, check.Equals, true)
	locked, err = AcquireApplicationLockWait(a.Name, "zzz", "/other", 500*time.Millisecond)
	c.Assert(err, check.IsNil)
	c.Assert(locked, check.Equals, false)
	app, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(app.Lock.Locked, check.Equals, true)
	c.Assert(app.Lock.Owner, check.Equals, "foo")
	c.Assert(app.Lock.Reason, check.Equals, "/something")
	c.Assert(app.Lock.AcquireDate, check.NotNil)
}

func (s *S) TestAppLockStringUnlocked(c *check.C) {
	lock := AppLock{Locked: false}
	c.Assert(lock.String(), check.Equals, "Not locked")
}

func (s *S) TestAppLockStringLocked(c *check.C) {
	lock := AppLock{
		Locked:      true,
		Reason:      "/app/my-app/deploy",
		Owner:       "someone",
		AcquireDate: time.Date(2048, time.November, 10, 10, 0, 0, 0, time.UTC),
	}
	c.Assert(lock.String(), check.Matches, "App locked by someone, running /app/my-app/deploy. Acquired in 2048-11-10T.*")
}

func (s *S) TestAppRegisterUnit(c *check.C) {
	a := App{Name: "appName", Platform: "python"}
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 3, "web", nil)
	units, err := a.Units()
	c.Assert(err, check.IsNil)
	var ips []string
	for _, u := range units {
		ips = append(ips, u.Ip)
	}
	customData := map[string]interface{}{"x": "y"}
	err = a.RegisterUnit(units[0].ID, customData)
	c.Assert(err, check.IsNil)
	units, err = a.Units()
	c.Assert(err, check.IsNil)
	c.Assert(units[0].Ip, check.Equals, ips[0]+"-updated")
	c.Assert(units[1].Ip, check.Equals, ips[1])
	c.Assert(units[2].Ip, check.Equals, ips[2])
	c.Assert(s.provisioner.CustomData(&a), check.DeepEquals, customData)
}

func (s *S) TestAppRegisterUnitInvalidUnit(c *check.C) {
	a := App{Name: "appName", Platform: "python"}
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	err := a.RegisterUnit("oddity", nil)
	c.Assert(err, check.NotNil)
	e, ok := err.(*provision.UnitNotFoundError)
	c.Assert(ok, check.Equals, true)
	c.Assert(e.ID, check.Equals, "oddity")
}

func (s *S) TestAppValidateTeamOwner(c *check.C) {
	team := auth.Team{Name: "test"}
	err := s.conn.Teams().Insert(team)
	defer s.conn.Teams().Remove(bson.M{"_id": team.Name})
	c.Assert(err, check.IsNil)
	a := App{Name: "test", Platform: "python", TeamOwner: team.Name}
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	err = a.validateTeamOwner()
	c.Assert(err, check.IsNil)
}

func (s *S) TestAppValidateTeamOwnerSetAnTeamWhichNotExists(c *check.C) {
	a := App{Name: "test", Platform: "python", TeamOwner: "not-exists"}
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	err := a.validateTeamOwner()
	c.Assert(err, check.Equals, auth.ErrTeamNotFound)
}

func (s *S) TestAppSetPoolByTeamOwner(c *check.C) {
	opts := provision.AddPoolOptions{Name: "test"}
	err := provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	defer provision.RemovePool("test")
	err = provision.AddTeamsToPool("test", []string{"test"})
	c.Assert(err, check.IsNil)
	app := App{
		Name:      "test",
		TeamOwner: "test",
	}
	err = app.SetPool()
	c.Assert(err, check.IsNil)
	c.Assert(app.Pool, check.Equals, "test")
}

func (s *S) TestAppSetPoolDefault(c *check.C) {
	opts := provision.AddPoolOptions{Name: "test", Public: true}
	err := provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	defer provision.RemovePool("test")
	app := App{
		Name: "test",
	}
	err = app.SetPool()
	c.Assert(err, check.IsNil)
	c.Assert(app.Pool, check.Equals, "pool1")
}

func (s *S) TestAppSetPoolByPool(c *check.C) {
	opts := provision.AddPoolOptions{Name: "test"}
	err := provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	defer provision.RemovePool("test")
	err = provision.AddTeamsToPool("test", []string{"test"})
	c.Assert(err, check.IsNil)
	opts = provision.AddPoolOptions{Name: "pool2"}
	err = provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	defer provision.RemovePool("pool2")
	err = provision.AddTeamsToPool("pool2", []string{"test"})
	c.Assert(err, check.IsNil)
	app := App{
		Name:      "test",
		Pool:      "pool2",
		TeamOwner: "test",
	}
	err = app.SetPool()
	c.Assert(err, check.IsNil)
	c.Assert(app.Pool, check.Equals, "pool2")
}

func (s *S) TestAppSetPoolManyPools(c *check.C) {
	opts := provision.AddPoolOptions{Name: "test"}
	err := provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	defer provision.RemovePool("test")
	err = provision.AddTeamsToPool("test", []string{"test"})
	c.Assert(err, check.IsNil)
	opts = provision.AddPoolOptions{Name: "pool2"}
	err = provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	defer provision.RemovePool("pool2")
	err = provision.AddTeamsToPool("pool2", []string{"test"})
	c.Assert(err, check.IsNil)
	app := App{
		Name:      "test",
		TeamOwner: "test",
	}
	err = app.SetPool()
	c.Assert(err, check.NotNil)
	c.Assert(err.Error(), check.Equals, "you have access to more than one pool, please choose one in app creation")
}

func (s *S) TestAppSetPoolNoDefault(c *check.C) {
	err := provision.RemovePool("pool1")
	c.Assert(err, check.IsNil)
	opts := provision.AddPoolOptions{Name: "pool1"}
	defer provision.AddPool(opts)
	app := App{
		Name: "test",
	}
	err = app.SetPool()
	c.Assert(err, check.NotNil)
	c.Assert(app.Pool, check.Equals, "")
}

func (s *S) TestAppSetPoolUserDontHaveAccessToPool(c *check.C) {
	opts := provision.AddPoolOptions{Name: "test"}
	err := provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	defer provision.RemovePool("test")
	err = provision.AddTeamsToPool("test", []string{"nopool"})
	c.Assert(err, check.IsNil)
	app := App{
		Name:      "test",
		TeamOwner: "test",
		Pool:      "test",
	}
	err = app.SetPool()
	c.Assert(err, check.NotNil)
	c.Assert(err.Error(), check.Equals, "You don't have access to pool test")
}

func (s *S) TestAppSetPoolToPublicPool(c *check.C) {
	opts := provision.AddPoolOptions{Name: "test", Public: true}
	err := provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	defer provision.RemovePool("test")
	app := App{
		Name:      "testapp",
		TeamOwner: "testapp",
		Pool:      "test",
	}
	err = app.SetPool()
	c.Assert(err, check.IsNil)
}

func (s *S) TestAppSetPoolPriorityTeamOwnerOverPublicPools(c *check.C) {
	opts := provision.AddPoolOptions{Name: "test", Public: true}
	err := provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	defer provision.RemovePool("public")
	opts = provision.AddPoolOptions{Name: "nonpublic"}
	err = provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	defer provision.RemovePool("nonpublic")
	err = provision.AddTeamsToPool("nonpublic", []string{"team1"})
	c.Assert(err, check.IsNil)
	a := App{
		Name:      "testapp",
		TeamOwner: "team1",
	}
	err = a.SetPool()
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(a)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	c.Assert(err, check.IsNil)
	app, _ := GetByName(a.Name)
	c.Assert("nonpublic", check.Equals, app.Pool)
}

func (s *S) TestAppChangePool(c *check.C) {
	opts := provision.AddPoolOptions{Name: "test"}
	err := provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	defer provision.RemovePool("test")
	err = provision.AddTeamsToPool("test", []string{"nopool"})
	c.Assert(err, check.IsNil)
	opts = provision.AddPoolOptions{Name: "test2"}
	err = provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	defer provision.RemovePool("test2")
	err = provision.AddTeamsToPool("test2", []string{"nopool"})
	c.Assert(err, check.IsNil)
	a := App{
		Name:      "test",
		TeamOwner: "nopool",
		Pool:      "test",
	}
	err = s.conn.Apps().Insert(a)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	c.Assert(err, check.IsNil)
	err = a.ChangePool("test2")
	c.Assert(err, check.IsNil)
	app, _ := GetByName(a.Name)
	c.Assert(app.Pool, check.Equals, "test2")
}

func (s *S) TestAppChangePoolNotExists(c *check.C) {
	opts := provision.AddPoolOptions{Name: "test"}
	err := provision.AddPool(opts)
	c.Assert(err, check.IsNil)
	defer provision.RemovePool("test")
	err = provision.AddTeamsToPool("test", []string{"nopool"})
	c.Assert(err, check.IsNil)
	app := App{
		Name:      "test",
		TeamOwner: "nopool",
		Pool:      "test",
	}
	err = s.conn.Apps().Insert(app)
	defer s.conn.Apps().Remove(bson.M{"name": app.Name})
	c.Assert(err, check.IsNil)
	err = app.ChangePool("test2")
	c.Assert(err, check.NotNil)
	c.Assert(err.Error(), check.Equals, "pool not found")
	c.Assert(app.Pool, check.Equals, "test")
}

func (s *S) TestShellToAnApp(c *check.C) {
	a := App{Name: "my-test-app"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 1, "web", nil)
	units, err := s.provisioner.Units(&a)
	c.Assert(err, check.IsNil)
	unit := units[0]
	buf := safe.NewBuffer([]byte("echo teste"))
	opts := provision.ShellOptions{
		Conn:   &provisiontest.FakeConn{Buf: buf},
		Width:  200,
		Height: 40,
		Unit:   unit.ID,
		Term:   "xterm",
	}
	err = a.Shell(opts)
	c.Assert(err, check.IsNil)
	expected := []provision.ShellOptions{opts}
	expected[0].App = &a
	c.Assert(s.provisioner.Shells(unit.ID), check.DeepEquals, expected)
}

func (s *S) TestAppMetricEnvs(c *check.C) {
	a := App{Name: "appName", Platform: "python", Pool: "mypool"}
	envs := a.MetricEnvs()
	expected := Provisioner.MetricEnvs(&a)
	c.Assert(envs, check.DeepEquals, expected)
}

func (s *S) TestChangePlan(c *check.C) {
	plan := Plan{Name: "something", Router: "fake-hc", CpuShare: 100, Memory: 268435456}
	err := s.conn.Plans().Insert(plan)
	c.Assert(err, check.IsNil)
	a := App{Name: "my-test-app", Plan: Plan{Router: "fake", Memory: 536870912, CpuShare: 50}}
	err = s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 3, "web", nil)
	c.Assert(routertest.FakeRouter.HasBackend(a.Name), check.Equals, true)
	c.Assert(routertest.HCRouter.HasBackend(a.Name), check.Equals, false)
	err = a.ChangePlan(plan.Name, new(bytes.Buffer))
	c.Assert(err, check.IsNil)
	app, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(app.Plan, check.DeepEquals, plan)
	c.Assert(s.provisioner.Restarts(app, ""), check.Equals, 1)
	c.Assert(routertest.FakeRouter.HasBackend(app.Name), check.Equals, false)
	c.Assert(routertest.HCRouter.HasBackend(app.Name), check.Equals, true)
	routes, err := routertest.HCRouter.Routes(app.Name)
	c.Assert(err, check.IsNil)
	routesStr := make([]string, len(routes))
	for i, route := range routes {
		routesStr[i] = route.String()
	}
	units, err := app.Units()
	c.Assert(err, check.IsNil)
	expected := make([]string, len(units))
	for i, unit := range units {
		expected[i] = unit.Address.String()
	}
	sort.Strings(routesStr)
	sort.Strings(expected)
	c.Assert(routesStr, check.DeepEquals, expected)
}

func (s *S) TestChangePlanNoRouteChange(c *check.C) {
	plan := Plan{Name: "something", Router: "fake", CpuShare: 100, Memory: 268435456}
	err := s.conn.Plans().Insert(plan)
	c.Assert(err, check.IsNil)
	a := App{Name: "my-test-app", Plan: Plan{Router: "fake", Memory: 536870912, CpuShare: 50}}
	err = s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 3, "web", nil)
	c.Assert(routertest.FakeRouter.HasBackend(a.Name), check.Equals, true)
	err = a.ChangePlan(plan.Name, new(bytes.Buffer))
	c.Assert(err, check.IsNil)
	app, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(app.Plan, check.DeepEquals, plan)
	c.Assert(s.provisioner.Restarts(app, ""), check.Equals, 1)
	c.Assert(routertest.FakeRouter.HasBackend(app.Name), check.Equals, true)
	routes, err := routertest.FakeRouter.Routes(app.Name)
	c.Assert(err, check.IsNil)
	routesStr := make([]string, len(routes))
	for i, route := range routes {
		routesStr[i] = route.String()
	}
	units, err := app.Units()
	c.Assert(err, check.IsNil)
	expected := make([]string, len(units))
	for i, unit := range units {
		expected[i] = unit.Address.String()
	}
	sort.Strings(routesStr)
	sort.Strings(expected)
	c.Assert(routesStr, check.DeepEquals, expected)
}

func (s *S) TestChangePlanNotFound(c *check.C) {
	var app App
	err := app.ChangePlan("some-unknown-plan", new(bytes.Buffer))
	c.Assert(err, check.Equals, ErrPlanNotFound)
}

func (s *S) TestChangePlanBackendRemovalFailure(c *check.C) {
	plan := Plan{Name: "something", Router: "fake-hc", CpuShare: 100, Memory: 268435456}
	err := s.conn.Plans().Insert(plan)
	c.Assert(err, check.IsNil)
	a := App{Name: "my-test-app", Plan: Plan{Name: "wrong", Router: "fakee", Memory: 536870912, CpuShare: 50}}
	err = s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 3, "web", nil)
	c.Assert(routertest.FakeRouter.HasBackend(a.Name), check.Equals, true)
	c.Assert(routertest.HCRouter.HasBackend(a.Name), check.Equals, false)
	err = a.ChangePlan(plan.Name, new(bytes.Buffer))
	c.Assert(err, check.IsNil)
	app, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(app.Plan.Name, check.Equals, "something")
	c.Assert(s.provisioner.Restarts(app, ""), check.Equals, 1)
	// Yeah, a test-ensured inconsistency.
	c.Assert(routertest.FakeRouter.HasBackend(app.Name), check.Equals, true)
	c.Assert(routertest.HCRouter.HasBackend(app.Name), check.Equals, true)
	routes, err := routertest.FakeRouter.Routes(app.Name)
	c.Assert(err, check.IsNil)
	routesStr := make([]string, len(routes))
	for i, route := range routes {
		routesStr[i] = route.String()
	}
	units, err := app.Units()
	c.Assert(err, check.IsNil)
	expected := make([]string, len(units))
	for i, unit := range units {
		expected[i] = unit.Address.String()
	}
	sort.Strings(routesStr)
	sort.Strings(expected)
	c.Assert(routesStr, check.DeepEquals, expected)
}

func (s *S) TestChangePlanRestartFailure(c *check.C) {
	plan := Plan{Name: "something", Router: "fake-hc", CpuShare: 100, Memory: 268435456}
	err := s.conn.Plans().Insert(plan)
	c.Assert(err, check.IsNil)
	a := App{Name: "my-test-app", Ip: "old-address", Plan: Plan{Name: "old", Router: "fake", Memory: 536870912, CpuShare: 50}}
	err = s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 3, "web", nil)
	c.Assert(routertest.FakeRouter.HasBackend(a.Name), check.Equals, true)
	c.Assert(routertest.HCRouter.HasBackend(a.Name), check.Equals, false)
	s.provisioner.PrepareFailure("Restart", fmt.Errorf("cannot restart app, I'm sorry"))
	err = a.ChangePlan(plan.Name, new(bytes.Buffer))
	c.Assert(err, check.NotNil)
	app, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(app.Plan.Name, check.Equals, "old")
	c.Assert(app.Ip, check.Equals, "old-address")
	c.Assert(s.provisioner.Restarts(app, ""), check.Equals, 0)
	c.Assert(routertest.FakeRouter.HasBackend(app.Name), check.Equals, true)
	c.Assert(routertest.HCRouter.HasBackend(app.Name), check.Equals, false)
	routes, err := routertest.FakeRouter.Routes(app.Name)
	c.Assert(err, check.IsNil)
	routesStr := make([]string, len(routes))
	for i, route := range routes {
		routesStr[i] = route.String()
	}
	units, err := app.Units()
	c.Assert(err, check.IsNil)
	expected := make([]string, len(units))
	for i, unit := range units {
		expected[i] = unit.Address.String()
	}
	sort.Strings(routesStr)
	sort.Strings(expected)
	c.Assert(routesStr, check.DeepEquals, expected)
}

func (s *S) TestChangePlanNoRestartNeeded(c *check.C) {
	plan := Plan{Name: "something", Router: "fake-hc", CpuShare: 50, Memory: 536870912}
	err := s.conn.Plans().Insert(plan)
	c.Assert(err, check.IsNil)
	a := App{Name: "my-test-app", Plan: Plan{Router: "fake", Memory: 536870912, CpuShare: 50}}
	err = s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 3, "web", nil)
	c.Assert(routertest.FakeRouter.HasBackend(a.Name), check.Equals, true)
	c.Assert(routertest.HCRouter.HasBackend(a.Name), check.Equals, false)
	err = a.ChangePlan(plan.Name, new(bytes.Buffer))
	c.Assert(err, check.IsNil)
	app, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(app.Plan, check.DeepEquals, plan)
	c.Assert(s.provisioner.Restarts(app, ""), check.Equals, 0)
	c.Assert(routertest.FakeRouter.HasBackend(app.Name), check.Equals, false)
	c.Assert(routertest.HCRouter.HasBackend(app.Name), check.Equals, true)
}

func (s *S) TestRebuildRoutes(c *check.C) {
	a := App{Name: "my-test-app", Plan: Plan{Router: "fake"}}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 3, "web", nil)
	units, err := a.Units()
	c.Assert(err, check.IsNil)
	routertest.FakeRouter.RemoveRoute(a.Name, units[2].Address)
	routertest.FakeRouter.AddRoute(a.Name, &url.URL{Scheme: "http", Host: "invalid:1234"})
	changes, err := a.RebuildRoutes()
	c.Assert(err, check.IsNil)
	c.Assert(changes.Added, check.DeepEquals, []string{units[2].Address.String()})
	c.Assert(changes.Removed, check.DeepEquals, []string{"http://invalid:1234"})
	routes, err := routertest.FakeRouter.Routes(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(routes, check.HasLen, 3)
	c.Assert(routertest.FakeRouter.HasRoute(a.Name, units[0].Address.String()), check.Equals, true)
	c.Assert(routertest.FakeRouter.HasRoute(a.Name, units[1].Address.String()), check.Equals, true)
	c.Assert(routertest.FakeRouter.HasRoute(a.Name, units[2].Address.String()), check.Equals, true)
	app, err := GetByName(a.Name)
	c.Assert(err, check.IsNil)
	addr, err := routertest.FakeRouter.Addr(app.Name)
	c.Assert(err, check.IsNil)
	c.Assert(app.Ip, check.Equals, addr)
}

type URLList []*url.URL

func (l URLList) Len() int           { return len(l) }
func (l URLList) Swap(i, j int)      { l[i], l[j] = l[j], l[i] }
func (l URLList) Less(i, j int) bool { return l[i].String() < l[j].String() }

func (s *S) TestRebuildRoutesAfterSwap(c *check.C) {
	a1 := App{Name: "my-test-app-1", Plan: Plan{Router: "fake"}}
	a2 := App{Name: "my-test-app-2", Plan: Plan{Router: "fake"}}
	err := s.conn.Apps().Insert(a1)
	c.Assert(err, check.IsNil)
	err = s.conn.Apps().Insert(a2)
	c.Assert(err, check.IsNil)
	err = s.provisioner.Provision(&a1)
	c.Assert(err, check.IsNil)
	err = s.provisioner.Provision(&a2)
	c.Assert(err, check.IsNil)
	defer s.provisioner.Destroy(&a1)
	defer s.provisioner.Destroy(&a2)
	s.provisioner.AddUnits(&a1, 3, "web", nil)
	s.provisioner.AddUnits(&a2, 2, "web", nil)
	units1, err := a1.Units()
	c.Assert(err, check.IsNil)
	units2, err := a2.Units()
	c.Assert(err, check.IsNil)
	routertest.FakeRouter.AddRoute(a1.Name, &url.URL{Scheme: "http", Host: "invalid:1234"})
	routertest.FakeRouter.RemoveRoute(a2.Name, units2[0].Address)
	err = Swap(&a1, &a2)
	c.Assert(err, check.IsNil)
	changes1, err := a1.RebuildRoutes()
	c.Assert(err, check.IsNil)
	changes2, err := a2.RebuildRoutes()
	c.Assert(err, check.IsNil)
	c.Assert(changes1.Added, check.IsNil)
	c.Assert(changes1.Removed, check.DeepEquals, []string{"http://invalid:1234"})
	c.Assert(changes2.Added, check.DeepEquals, []string{units2[0].Address.String()})
	c.Assert(changes2.Removed, check.IsNil)
	routes1, err := routertest.FakeRouter.Routes(a1.Name)
	c.Assert(err, check.IsNil)
	routes2, err := routertest.FakeRouter.Routes(a2.Name)
	c.Assert(err, check.IsNil)
	sort.Sort(URLList(routes1))
	sort.Sort(URLList(routes2))
	c.Assert(routes1, check.DeepEquals, []*url.URL{
		units1[0].Address,
		units1[1].Address,
		units1[2].Address,
	})
	c.Assert(routes2, check.DeepEquals, []*url.URL{
		units2[0].Address,
		units2[1].Address,
	})
	c.Assert(routertest.FakeRouter.HasRoute(a1.Name, units2[0].Address.String()), check.Equals, true)
	c.Assert(routertest.FakeRouter.HasRoute(a1.Name, units2[1].Address.String()), check.Equals, true)
	c.Assert(routertest.FakeRouter.HasRoute(a2.Name, units1[0].Address.String()), check.Equals, true)
	c.Assert(routertest.FakeRouter.HasRoute(a2.Name, units1[1].Address.String()), check.Equals, true)
	c.Assert(routertest.FakeRouter.HasRoute(a2.Name, units1[2].Address.String()), check.Equals, true)
}

func (s *S) TestRebuildRoutesRecreatesBackend(c *check.C) {
	a := App{Name: "my-test-app", Plan: Plan{Router: "fake"}}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 3, "web", nil)
	units, err := a.Units()
	c.Assert(err, check.IsNil)
	routertest.FakeRouter.RemoveBackend(a.Name)
	changes, err := a.RebuildRoutes()
	c.Assert(err, check.IsNil)
	sort.Strings(changes.Added)
	c.Assert(changes.Added, check.DeepEquals, []string{
		units[0].Address.String(),
		units[1].Address.String(),
		units[2].Address.String(),
	})
	routes, err := routertest.FakeRouter.Routes(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(routes, check.HasLen, 3)
	c.Assert(routertest.FakeRouter.HasRoute(a.Name, units[0].Address.String()), check.Equals, true)
	c.Assert(routertest.FakeRouter.HasRoute(a.Name, units[1].Address.String()), check.Equals, true)
	c.Assert(routertest.FakeRouter.HasRoute(a.Name, units[2].Address.String()), check.Equals, true)
}

func (s *S) TestRebuildRoutesRecreatesCnames(c *check.C) {
	a := App{Name: "my-test-app", Plan: Plan{Router: "fake"}}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	err = s.provisioner.Provision(&a)
	c.Assert(err, check.IsNil)
	defer s.provisioner.Destroy(&a)
	s.provisioner.AddUnits(&a, 1, "web", nil)
	units, err := a.Units()
	c.Assert(err, check.IsNil)
	err = a.AddCName("my.cname.com")
	c.Assert(err, check.IsNil)
	c.Assert(routertest.FakeRouter.HasCName("my.cname.com"), check.Equals, true)
	err = routertest.FakeRouter.UnsetCName("my.cname.com", a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(routertest.FakeRouter.HasCName("my.cname.com"), check.Equals, false)
	changes, err := a.RebuildRoutes()
	c.Assert(err, check.IsNil)
	c.Assert(changes, check.DeepEquals, &RebuildRoutesResult{})
	routes, err := routertest.FakeRouter.Routes(a.Name)
	c.Assert(err, check.IsNil)
	c.Assert(routes, check.HasLen, 1)
	c.Assert(routertest.FakeRouter.HasRoute(a.Name, units[0].Address.String()), check.Equals, true)
	c.Assert(routertest.FakeRouter.HasCName("my.cname.com"), check.Equals, true)
}

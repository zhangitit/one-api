package router

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Exercise the native control API and the actual selector, including retry exclusion.
func TestZeoNexusHigherPriorityAndFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config.ZeoNexusEnabled = true
	config.ZeoNexusProfile = "inference"
	config.ZeoNexusControlSecret = strings.Repeat("c", 40)
	config.ZeoNexusMasterKey = strings.Repeat("m", 40)
	config.ZeoNexusAllowedUpstreamHosts = []string{"127.0.0.1"}
	config.ZeoNexusAllowInsecureHTTP = true
	db, err := gorm.Open(sqlite.Open("file:zeonexus-priority?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	model.DB, model.LOG_DB = db, db
	if err = db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{}); err != nil {
		t.Fatal(err)
	}
	if err = model.MigrateZeoNexus(); err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	SetZeoNexusRouter(engine)
	for _, item := range []struct {
		ref      string
		priority int
	}{{"low", -10}, {"high", 10}} {
		controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/channels/"+item.ref, map[string]any{
			"name": item.ref, "type": 50, "base_url": "http://127.0.0.1:1/v1", "key": "fixture-only",
			"models": []string{"priority-model"}, "model_mapping": map[string]string{"priority-model": "upstream"},
			"status": "active", "weight": 1, "priority": item.priority, "profile": "inference", "revision": 1,
		}, http.StatusOK)
	}
	controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/routes/priority-route", map[string]any{
		"profile": "inference", "model": "priority-model", "mode": "chat", "channel_ids": "low,high",
		"strategy": "priority", "status": "active", "revision": 1,
	}, http.StatusOK)
	var high, low model.ZeoManagedChannel
	if err = db.Where("external_id = ?", "high").First(&high).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Where("external_id = ?", "low").First(&low).Error; err != nil {
		t.Fatal(err)
	}
	selected, err := model.GetZeoSatisfiedChannel("priority-model", "inference", "", 0)
	if err != nil || selected.Id != high.ChannelId {
		t.Fatalf("priority 10 must precede -10: %v %+v", err, selected)
	}
	selected, err = model.GetZeoSatisfiedChannel("priority-model", "inference", "", high.ChannelId)
	if err != nil || selected.Id != low.ChannelId {
		t.Fatalf("retry must choose remaining lower-priority channel: %v %+v", err, selected)
	}
	if err = db.Model(&model.Channel{}).Where("id = ?", high.ChannelId).Update("status", model.ChannelStatusManuallyDisabled).Error; err != nil {
		t.Fatal(err)
	}
	selected, err = model.GetZeoSatisfiedChannel("priority-model", "inference", "", 0)
	if err != nil || selected.Id != low.ChannelId {
		t.Fatalf("disabled high-priority channel must not be selected: %v %+v", err, selected)
	}
}

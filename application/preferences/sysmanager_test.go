package preferences

import (
	"context"
	"reflect"
	"testing"

	domain "github.com/mslxl/natcatty/domain/preferences"
)

func TestSysManagerIntervalsPersistThroughTheStore(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	if err := service.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := service.Settings().SysManager; !reflect.DeepEqual(got, domain.DefaultSysManager()) {
		t.Fatalf("defaults = %+v", got)
	}
	if err := service.Update(func(settings *domain.Settings) {
		settings.SysManager = settings.SysManager.WithInterval(
			domain.SysManagerProcesses, 10)
		settings.SysManager = settings.SysManager.WithInterval(
			domain.SysManagerServices, 30)
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if store.saves != 1 {
		t.Fatalf("saves = %d, want 1", store.saves)
	}

	reloaded := NewService(store)
	if err := reloaded.Load(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	manager := reloaded.Settings().SysManager
	if manager.ProcessesInterval != 10 || manager.ServicesInterval != 30 {
		t.Errorf("reloaded intervals = %+v", manager)
	}
	if manager.OverviewInterval != 3 || manager.PortsInterval != 3 {
		t.Errorf("unrelated intervals changed: %+v", manager)
	}
	if err := reloaded.ResetGroup(domain.GroupSysManager); err != nil {
		t.Fatalf("ResetGroup: %v", err)
	}
	if got := reloaded.Settings().SysManager; !reflect.DeepEqual(got, domain.DefaultSysManager()) {
		t.Errorf("sysmanager reset = %+v", got)
	}
}

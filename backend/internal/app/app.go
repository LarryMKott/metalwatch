// Package app 持有服务端运行时的依赖容器。
//
// 之所以单独成包：HTTP 层（api/web、api/agentpb）与任务层都需要这些依赖，
// 若把容器放在任一 HTTP 子包里会形成 import 环。
package app

import (
	"log/slog"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/internal/task"
	"github.com/LarryMKott/metalwatch/pkg/config"
)

// Deps 是应用运行期的依赖集合。
type Deps struct {
	Config  config.Config
	Store   adapter.MetadataStore
	TSDB    adapter.TimeSeriesStore
	Hosts   *service.HostService
	Pool    *task.Pool
	Log     *slog.Logger
	Version string
	Started time.Time
}

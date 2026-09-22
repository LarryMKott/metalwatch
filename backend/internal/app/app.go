// Package app 持有服务端运行时的依赖容器。
//
// 之所以单独成包：HTTP 层（api/web、api/agentpb）与任务层都需要这些依赖，
// 若把容器放在任一 HTTP 子包里会形成 import 环。
package app

import (
	"log/slog"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/pipeline"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/internal/task"
	"github.com/LarryMKott/metalwatch/internal/ws"
	"github.com/LarryMKott/metalwatch/pkg/config"
)

// Deps 是应用运行期的依赖集合。
type Deps struct {
	Config    config.Config
	MasterKey []byte // 凭据加密主密钥（W4；BMC 凭据接口用）
	Store     adapter.MetadataStore
	TSDB      adapter.TimeSeriesStore
	Pipeline  *pipeline.Pipeline
	Alerts    *service.AlertService
	Hosts     *service.HostService
	Pool      *task.Pool
	Hub       *ws.Hub                   // WebSocket 实时推送（W7）；可为 nil
	IPMI      *service.IPMIPoller       // 带外采集轮询器（W4）；可为 nil
	Inventory *service.InventoryService // 资产快照与变更检测（W2）
	Log       *slog.Logger
	Version   string
	Started   time.Time
}

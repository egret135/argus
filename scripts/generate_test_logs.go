// generate_test_logs.go — 生成 10 个日志文件，每个 20000 条，符合 Argus JSON Lines Schema
// 用法: go run scripts/generate_test_logs.go [-dir /tmp/argus-logs]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------- 服务定义 ----------

type serviceSpec struct {
	Name     string
	Lang     string // "php" or "go"
	Packages []string
	Files    []string
}

var services = []serviceSpec{
	{"order-api", "go", []string{"database/sql", "net/http", "encoding/json", "context", "sync"}, []string{"internal/order/handler.go", "internal/order/service.go", "internal/order/repo.go", "cmd/order/main.go", "pkg/db/pool.go"}},
	{"user-service", "go", []string{"gorm.io/gorm", "golang.org/x/crypto/bcrypt", "net/http", "context"}, []string{"internal/user/handler.go", "internal/user/model.go", "internal/user/auth.go", "cmd/user/main.go", "pkg/cache/redis.go"}},
	{"payment-gateway", "go", []string{"crypto/tls", "net/http", "encoding/json", "time", "io"}, []string{"internal/payment/stripe.go", "internal/payment/handler.go", "internal/payment/refund.go", "cmd/payment/main.go", "pkg/httpclient/client.go"}},
	{"notification-worker", "go", []string{"github.com/streadway/amqp", "net/smtp", "html/template", "context"}, []string{"internal/notify/consumer.go", "internal/notify/email.go", "internal/notify/sms.go", "cmd/notify/main.go", "pkg/mq/rabbitmq.go"}},
	{"inventory-sync", "go", []string{"database/sql", "encoding/csv", "net/http", "sync", "time"}, []string{"internal/inventory/sync.go", "internal/inventory/stockcheck.go", "internal/inventory/handler.go", "cmd/inventory/main.go", "pkg/warehouse/api.go"}},
	{"cms-backend", "php", []string{"Illuminate\\Database", "Illuminate\\Http", "Illuminate\\Cache", "Monolog\\Logger"}, []string{"app/Http/Controllers/ArticleController.php", "app/Models/Article.php", "app/Services/CacheService.php", "routes/api.php", "app/Exceptions/Handler.php"}},
	{"shop-api", "php", []string{"PDO", "Redis", "Memcached", "GuzzleHttp\\Client", "Monolog\\Logger"}, []string{"src/Controller/ProductController.php", "src/Repository/ProductRepository.php", "src/Service/CartService.php", "src/Middleware/AuthMiddleware.php", "public/index.php"}},
	{"crm-platform", "php", []string{"Doctrine\\ORM", "Symfony\\Component\\HttpFoundation", "Twig\\Environment"}, []string{"src/Controller/ContactController.php", "src/Entity/Contact.php", "src/Service/LeadScoring.php", "src/EventListener/AuditListener.php", "config/services.yaml"}},
	{"media-processor", "go", []string{"image/jpeg", "image/png", "os/exec", "io", "sync"}, []string{"internal/media/resize.go", "internal/media/transcode.go", "internal/media/upload.go", "cmd/media/main.go", "pkg/storage/s3.go"}},
	{"analytics-collector", "php", []string{"ClickHouseDB\\Client", "Predis\\Client", "Psr\\Log\\LoggerInterface", "JsonSchema\\Validator"}, []string{"src/Collector/EventCollector.php", "src/Pipeline/Aggregator.php", "src/Storage/ClickHouseWriter.php", "src/Validator/SchemaValidator.php", "bin/worker.php"}},
}

// ---------- 错误模板 ----------

type errorTemplate struct {
	Level      string
	MessageFmt string            // 含 %s 占位符
	ArgsGen    func(svc serviceSpec) []interface{}
	StackGen   func(svc serviceSpec) string
	ExtraGen   func(svc serviceSpec) map[string]interface{}
	CallerGen  func(svc serviceSpec) string
}

// ---------- Go stack trace 生成 ----------

func goStack(svc serviceSpec, depth int) string {
	var sb strings.Builder
	sb.WriteString("goroutine ")
	sb.WriteString(fmt.Sprintf("%d", rand.Intn(500)+1))
	sb.WriteString(" [running]:\n")
	for i := 0; i < depth; i++ {
		file := svc.Files[rand.Intn(len(svc.Files))]
		line := rand.Intn(300) + 10
		funcName := goFuncNames[rand.Intn(len(goFuncNames))]
		pkg := goPkgPrefixes[rand.Intn(len(goPkgPrefixes))]
		sb.WriteString(fmt.Sprintf("%s.%s(...)\n\t/app/%s:%d +0x%x\n", pkg, funcName, file, line, rand.Intn(0x1ff)+0x10))
	}
	return sb.String()
}

var goFuncNames = []string{
	"HandleRequest", "ServeHTTP", "processOrder", "validateInput",
	"executeQuery", "fetchFromCache", "retryWithBackoff", "marshal",
	"unmarshal", "sendNotification", "checkPermission", "loadConfig",
	"initPool", "acquireConn", "releaseConn", "publishMessage",
	"consumeMessage", "encodeResponse", "decodeRequest", "runMigration",
	"buildIndex", "flushBuffer", "compactData", "resolveEndpoint",
	"dialTCP", "handshakeTLS", "readBody", "writeResponse",
	"parseToken", "signJWT", "verifySignature", "hashPassword",
}

var goPkgPrefixes = []string{
	"main", "handler", "service", "repo", "middleware",
	"cache", "db", "mq", "http", "grpc",
}

// ---------- PHP stack trace 生成 ----------

func phpStack(svc serviceSpec, depth int) string {
	var sb strings.Builder
	sb.WriteString("PHP Fatal error: ")
	msgs := []string{
		"Uncaught TypeError: Argument 1 passed to %s must be of type string, null given",
		"Uncaught Error: Call to a member function %s on null",
		"Uncaught PDOException: SQLSTATE[HY000] [2002] Connection refused in %s",
		"Uncaught RuntimeException: Cache key too long in %s",
		"Allowed memory size of 134217728 bytes exhausted (tried to allocate %d bytes) in %s",
	}
	msg := msgs[rand.Intn(len(msgs))]
	file := svc.Files[rand.Intn(len(svc.Files))]
	if strings.Contains(msg, "%d") {
		sb.WriteString(fmt.Sprintf(msg, rand.Intn(64*1024*1024)+1024, file))
	} else {
		sb.WriteString(fmt.Sprintf(msg, file))
	}
	sb.WriteString("\nStack trace:\n")
	for i := 0; i < depth; i++ {
		f := svc.Files[rand.Intn(len(svc.Files))]
		line := rand.Intn(500) + 1
		cls := phpClassNames[rand.Intn(len(phpClassNames))]
		method := phpMethodNames[rand.Intn(len(phpMethodNames))]
		sb.WriteString(fmt.Sprintf("#%d %s(%d): %s->%s()\n", i, f, line, cls, method))
	}
	sb.WriteString(fmt.Sprintf("  thrown in %s on line %d", svc.Files[rand.Intn(len(svc.Files))], rand.Intn(500)+1))
	return sb.String()
}

var phpClassNames = []string{
	"App\\Controller\\BaseController", "App\\Service\\UserService",
	"App\\Repository\\OrderRepository", "App\\Middleware\\AuthMiddleware",
	"Illuminate\\Database\\Query\\Builder", "Illuminate\\Routing\\Router",
	"PDO", "Redis", "Memcached", "GuzzleHttp\\Client",
	"Doctrine\\ORM\\EntityManager", "Symfony\\Component\\HttpKernel\\Kernel",
	"App\\Service\\CacheManager", "App\\EventListener\\ExceptionHandler",
}

var phpMethodNames = []string{
	"execute", "fetch", "findById", "save", "delete",
	"validate", "authorize", "handle", "process", "dispatch",
	"render", "serialize", "deserialize", "connect", "disconnect",
	"get", "set", "publish", "consume", "transform",
}

// ---------- 错误消息池 ----------

var goErrorMessages = []errorTemplate{
	// Database errors
	{Level: "ERROR", MessageFmt: "database connection pool exhausted: max %d connections reached, %d waiting", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(50) + 10, rand.Intn(200) + 1} }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(4)+3) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"pool_size": rand.Intn(50) + 10, "active_conns": rand.Intn(50) + 10, "wait_count": rand.Intn(200)} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "ERROR", MessageFmt: "sql: query execution failed: pq: deadlock detected on table %q", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{dbTables[rand.Intn(len(dbTables))]} }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(3)+2) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"table": dbTables[rand.Intn(len(dbTables))], "duration_ms": rand.Intn(5000) + 100} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "FATAL", MessageFmt: "database migration failed at version %d: %s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(100) + 1, dbMigrationErrors[rand.Intn(len(dbMigrationErrors))]} }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(5)+4) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"migration_version": rand.Intn(100) + 1} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "ERROR", MessageFmt: "sql: connection reset by peer during transaction on %q", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{dbTables[rand.Intn(len(dbTables))]} }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(3)+2) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"tx_duration_ms": rand.Intn(30000) + 500} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "WARN", MessageFmt: "slow query detected: %dms on table %s (threshold: 500ms)", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(10000) + 500, dbTables[rand.Intn(len(dbTables))]} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"query_hash": fmt.Sprintf("0x%08x", rand.Uint32()), "rows_examined": rand.Intn(100000)} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},

	// HTTP / Network errors
	{Level: "ERROR", MessageFmt: "upstream service %s returned HTTP %d: %s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{upstreamServices[rand.Intn(len(upstreamServices))], httpErrorCodes[rand.Intn(len(httpErrorCodes))], httpErrorBodies[rand.Intn(len(httpErrorBodies))]} }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(3)+2) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"url": fmt.Sprintf("https://%s.internal/api/v1/%s", upstreamServices[rand.Intn(len(upstreamServices))], apiPaths[rand.Intn(len(apiPaths))]), "latency_ms": rand.Intn(30000) + 100} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "ERROR", MessageFmt: "dial tcp %s:%d: connect: connection refused", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{randomIP(), rand.Intn(9000) + 1000} }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(4)+3) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"retry_count": rand.Intn(5), "service": upstreamServices[rand.Intn(len(upstreamServices))]} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "ERROR", MessageFmt: "context deadline exceeded after %dms waiting for %s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(30000) + 1000, upstreamServices[rand.Intn(len(upstreamServices))]} }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(3)+2) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"timeout_config_ms": rand.Intn(10000) + 1000} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "WARN", MessageFmt: "TLS handshake timeout with %s after %dms", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{randomIP(), rand.Intn(10000) + 500} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"tls_version": "1.3"} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "ERROR", MessageFmt: "http: request body too large: %d bytes exceeds limit %d", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(100*1024*1024) + 10*1024*1024, 10 * 1024 * 1024} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"method": httpMethods[rand.Intn(len(httpMethods))], "path": "/" + apiPaths[rand.Intn(len(apiPaths))]} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},

	// Nil pointer / panic
	{Level: "FATAL", MessageFmt: "runtime error: invalid memory address or nil pointer dereference", ArgsGen: func(s serviceSpec) []interface{} { return nil }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(6)+5) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"signal": "SIGSEGV", "goroutine_id": rand.Intn(500)} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "FATAL", MessageFmt: "panic: runtime error: index out of range [%d] with length %d", ArgsGen: func(s serviceSpec) []interface{} { n := rand.Intn(100); return []interface{}{n + rand.Intn(50) + 1, n} }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(5)+4) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"recovered": false} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "FATAL", MessageFmt: "panic: send on closed channel", ArgsGen: func(s serviceSpec) []interface{} { return nil }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(5)+3) }, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "FATAL", MessageFmt: "panic: assignment to entry in nil map", ArgsGen: func(s serviceSpec) []interface{} { return nil }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(4)+3) }, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "ERROR", MessageFmt: "recovered from panic in handler %s: %s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{"/" + apiPaths[rand.Intn(len(apiPaths))], goPanicMsgs[rand.Intn(len(goPanicMsgs))]} }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(6)+3) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"recovered": true} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},

	// Cache / Redis
	{Level: "ERROR", MessageFmt: "redis: connection pool timeout after %dms, pool size %d", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(5000) + 500, rand.Intn(50) + 10} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"redis_addr": fmt.Sprintf("redis-%d.internal:6379", rand.Intn(3)+1)} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "WARN", MessageFmt: "cache miss for key %q, falling back to database", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{cacheKeys[rand.Intn(len(cacheKeys))] + fmt.Sprintf(":%d", rand.Intn(100000))} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"cache_ttl": rand.Intn(3600), "hit_rate": fmt.Sprintf("%.2f%%", rand.Float64()*100)} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "ERROR", MessageFmt: "redis CLUSTER MOVED %d %s:%d — key resharding in progress", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(16384), randomIP(), 6379} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},

	// Auth / JWT
	{Level: "WARN", MessageFmt: "JWT token expired for user %s, issued at %s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{usernames[rand.Intn(len(usernames))], time.Now().Add(-time.Duration(rand.Intn(48)+1) * time.Hour).Format(time.RFC3339)} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"user_id": rand.Intn(100000), "ip": randomIP()} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "ERROR", MessageFmt: "bcrypt: password hash comparison failed for user %q", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{usernames[rand.Intn(len(usernames))]} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"attempts": rand.Intn(10) + 1, "ip": randomIP()} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "WARN", MessageFmt: "rate limit exceeded for IP %s: %d requests in %ds window", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{randomIP(), rand.Intn(500) + 100, rand.Intn(60) + 10} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},

	// JSON / encoding
	{Level: "ERROR", MessageFmt: "json: cannot unmarshal %s into Go value of type %s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{jsonTypes[rand.Intn(len(jsonTypes))], goTypes[rand.Intn(len(goTypes))]} }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(3)+2) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"request_id": randomHex(16)} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "ERROR", MessageFmt: "proto: required field %q not set in %s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{protoFields[rand.Intn(len(protoFields))], protoMessages[rand.Intn(len(protoMessages))]} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},

	// Goroutine / concurrency
	{Level: "WARN", MessageFmt: "goroutine count %d exceeds threshold %d, possible leak", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(10000) + 5000, 5000} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"heap_mb": rand.Intn(512) + 64} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "ERROR", MessageFmt: "sync: WaitGroup counter went negative in %s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{s.Files[rand.Intn(len(s.Files))]} }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(4)+3) }, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "ERROR", MessageFmt: "context canceled: operation aborted by caller after %dms", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(10000) + 100} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"operation": goOperations[rand.Intn(len(goOperations))]} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},

	// File / IO
	{Level: "ERROR", MessageFmt: "open %s: too many open files (ulimit: %d)", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{filePaths[rand.Intn(len(filePaths))], rand.Intn(1024) + 256} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"fd_count": rand.Intn(1024) + 256} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "ERROR", MessageFmt: "write %s: no space left on device", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{filePaths[rand.Intn(len(filePaths))]} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"disk_usage_pct": rand.Intn(5) + 96} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "WARN", MessageFmt: "disk usage at %d%% on %s, approaching capacity", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(15) + 85, diskMounts[rand.Intn(len(diskMounts))]} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},

	// Message queue
	{Level: "ERROR", MessageFmt: "amqp: channel/connection closed unexpectedly: %s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{amqpErrors[rand.Intn(len(amqpErrors))]} }, StackGen: func(s serviceSpec) string { return goStack(s, rand.Intn(3)+2) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"queue": mqQueues[rand.Intn(len(mqQueues))], "reconnect_count": rand.Intn(10)} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "WARN", MessageFmt: "message queue %s consumer lag: %d messages behind", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{mqQueues[rand.Intn(len(mqQueues))], rand.Intn(50000) + 1000} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},

	// INFO / DEBUG 正常日志
	{Level: "INFO", MessageFmt: "request completed: %s %s — %d in %dms", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{httpMethods[rand.Intn(len(httpMethods))], "/" + apiPaths[rand.Intn(len(apiPaths))], httpOKCodes[rand.Intn(len(httpOKCodes))], rand.Intn(500) + 1} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"user_id": rand.Intn(100000), "request_id": randomHex(16)} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "INFO", MessageFmt: "database connection established to %s:%d, pool size %d", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{randomIP(), rand.Intn(1000) + 3306, rand.Intn(30) + 5} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "DEBUG", MessageFmt: "cache SET %s ttl=%ds size=%d bytes", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{cacheKeys[rand.Intn(len(cacheKeys))] + fmt.Sprintf(":%d", rand.Intn(100000)), rand.Intn(3600) + 60, rand.Intn(10240) + 64} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "INFO", MessageFmt: "scheduled task %s completed in %dms, processed %d items", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{cronJobs[rand.Intn(len(cronJobs))], rand.Intn(60000) + 100, rand.Intn(10000) + 1} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "DEBUG", MessageFmt: "gRPC call %s/%s completed in %dms", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{grpcServices[rand.Intn(len(grpcServices))], grpcMethods[rand.Intn(len(grpcMethods))], rand.Intn(200) + 1} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "INFO", MessageFmt: "worker %d started processing batch of %d events", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(16) + 1, rand.Intn(1000) + 10} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "INFO", MessageFmt: "health check passed: cpu=%.1f%% mem=%.1f%% goroutines=%d", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Float64()*80 + 5, rand.Float64()*70 + 10, rand.Intn(500) + 10} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
	{Level: "DEBUG", MessageFmt: "resolving DNS for %s.internal: %s (%dms)", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{upstreamServices[rand.Intn(len(upstreamServices))], randomIP(), rand.Intn(50) + 1} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(200)+1) }},
}

var phpErrorMessages = []errorTemplate{
	// PDO / Database
	{Level: "ERROR", MessageFmt: "PDOException: SQLSTATE[HY000] [2002] Connection refused (SQL: SELECT * FROM %s WHERE id = %d)", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{dbTables[rand.Intn(len(dbTables))], rand.Intn(100000)} }, StackGen: func(s serviceSpec) string { return phpStack(s, rand.Intn(5)+3) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"sqlstate": "HY000", "driver_code": 2002} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "ERROR", MessageFmt: "PDOException: SQLSTATE[23000]: Integrity constraint violation: 1062 Duplicate entry '%d' for key '%s.PRIMARY'", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(100000), dbTables[rand.Intn(len(dbTables))]} }, StackGen: func(s serviceSpec) string { return phpStack(s, rand.Intn(4)+2) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"sqlstate": "23000", "driver_code": 1062} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "ERROR", MessageFmt: "QueryException: SQLSTATE[42S02]: Base table or view not found: 1146 Table '%s' doesn't exist", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{dbTables[rand.Intn(len(dbTables))] + "_backup"} }, StackGen: func(s serviceSpec) string { return phpStack(s, rand.Intn(4)+3) }, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "FATAL", MessageFmt: "PDOException: SQLSTATE[HY000] [1045] Access denied for user 'app'@'%s' (using password: YES)", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{randomIP()} }, StackGen: func(s serviceSpec) string { return phpStack(s, rand.Intn(5)+4) }, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "ERROR", MessageFmt: "PDOException: SQLSTATE[40001]: Serialization failure: 1213 Deadlock found when trying to get lock on table %s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{dbTables[rand.Intn(len(dbTables))]} }, StackGen: func(s serviceSpec) string { return phpStack(s, rand.Intn(3)+2) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"retry_count": rand.Intn(3)} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},

	// Null / TypeError
	{Level: "FATAL", MessageFmt: "TypeError: Argument #%d passed to %s::%s() must be of type %s, null given", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(5) + 1, phpClassNames[rand.Intn(len(phpClassNames))], phpMethodNames[rand.Intn(len(phpMethodNames))], phpTypes[rand.Intn(len(phpTypes))]} }, StackGen: func(s serviceSpec) string { return phpStack(s, rand.Intn(5)+3) }, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "FATAL", MessageFmt: "Error: Call to a member function %s() on null", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{phpMethodNames[rand.Intn(len(phpMethodNames))]} }, StackGen: func(s serviceSpec) string { return phpStack(s, rand.Intn(6)+4) }, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "ERROR", MessageFmt: "Undefined property: %s::$%s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{phpClassNames[rand.Intn(len(phpClassNames))], phpProperties[rand.Intn(len(phpProperties))]} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "ERROR", MessageFmt: "Undefined array key %q in %s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{phpArrayKeys[rand.Intn(len(phpArrayKeys))], s.Files[rand.Intn(len(s.Files))]} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},

	// Memory / OOM
	{Level: "FATAL", MessageFmt: "Allowed memory size of %d bytes exhausted (tried to allocate %d bytes)", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{134217728, rand.Intn(64*1024*1024) + 1024} }, StackGen: func(s serviceSpec) string { return phpStack(s, rand.Intn(5)+3) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"memory_limit": "128M"} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "WARN", MessageFmt: "memory usage at %.1fMB approaching limit 128MB in %s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Float64()*30 + 100, s.Files[rand.Intn(len(s.Files))]} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},

	// Session / Redis
	{Level: "ERROR", MessageFmt: "RedisException: Connection refused [tcp://%s:%d]", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{randomIP(), 6379} }, StackGen: func(s serviceSpec) string { return phpStack(s, rand.Intn(3)+2) }, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "ERROR", MessageFmt: "session_start(): Failed to read session data: redis (path: tcp://%s:%d)", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{randomIP(), 6379} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},

	// Syntax / parse
	{Level: "FATAL", MessageFmt: "Parse error: syntax error, unexpected '%s', expecting '%s' in %s on line %d", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{phpSyntaxTokens[rand.Intn(len(phpSyntaxTokens))], phpExpectTokens[rand.Intn(len(phpExpectTokens))], s.Files[rand.Intn(len(s.Files))], rand.Intn(500) + 1} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "FATAL", MessageFmt: "Fatal error: Cannot redeclare class %s (previously declared in %s:%d)", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{phpClassNames[rand.Intn(len(phpClassNames))], s.Files[rand.Intn(len(s.Files))], rand.Intn(500) + 1} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},

	// HTTP / Guzzle
	{Level: "ERROR", MessageFmt: "GuzzleHttp\\Exception\\ConnectException: cURL error 28: Connection timed out after %dms (see https://curl.haxx.se/libcurl/c/libcurl-errors.html)", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(30000) + 3000} }, StackGen: func(s serviceSpec) string { return phpStack(s, rand.Intn(4)+3) }, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"url": fmt.Sprintf("https://%s.internal/api/%s", upstreamServices[rand.Intn(len(upstreamServices))], apiPaths[rand.Intn(len(apiPaths))])} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "ERROR", MessageFmt: "GuzzleHttp\\Exception\\ServerException: Server error: `POST https://%s.internal/api/%s` resulted in a `%d Internal Server Error`", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{upstreamServices[rand.Intn(len(upstreamServices))], apiPaths[rand.Intn(len(apiPaths))], httpErrorCodes[rand.Intn(len(httpErrorCodes))]} }, StackGen: func(s serviceSpec) string { return phpStack(s, rand.Intn(3)+2) }, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},

	// Composer / Autoload
	{Level: "FATAL", MessageFmt: "Fatal error: Uncaught Error: Class '%s' not found in %s:%d", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{s.Packages[rand.Intn(len(s.Packages))] + "\\" + phpClassSuffixes[rand.Intn(len(phpClassSuffixes))], s.Files[rand.Intn(len(s.Files))], rand.Intn(500) + 1} }, StackGen: func(s serviceSpec) string { return phpStack(s, rand.Intn(4)+2) }, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},

	// JSON decode
	{Level: "ERROR", MessageFmt: "json_decode(): Syntax error, malformed JSON at offset %d in %s", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{rand.Intn(1000), s.Files[rand.Intn(len(s.Files))]} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"json_error": 4} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},

	// File
	{Level: "WARN", MessageFmt: "file_put_contents(%s): failed to open stream: Permission denied", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{phpFilePaths[rand.Intn(len(phpFilePaths))]} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},

	// INFO / DEBUG
	{Level: "INFO", MessageFmt: "[%s] %s %s — %d (%dms)", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{time.Now().Format("2006-01-02"), httpMethods[rand.Intn(len(httpMethods))], "/" + apiPaths[rand.Intn(len(apiPaths))], httpOKCodes[rand.Intn(len(httpOKCodes))], rand.Intn(500) + 1} }, StackGen: nil, ExtraGen: func(s serviceSpec) map[string]interface{} { return map[string]interface{}{"user_id": rand.Intn(100000)} }, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "DEBUG", MessageFmt: "Eloquent query: SELECT * FROM %s WHERE %s = ? [%d] (%.2fms)", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{dbTables[rand.Intn(len(dbTables))], dbColumns[rand.Intn(len(dbColumns))], rand.Intn(100000), rand.Float64()*100 + 0.1} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "INFO", MessageFmt: "artisan command %s completed in %.2fs", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{artisanCommands[rand.Intn(len(artisanCommands))], rand.Float64()*60 + 0.1} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "INFO", MessageFmt: "queue job %s processed successfully, %d remaining", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{phpQueueJobs[rand.Intn(len(phpQueueJobs))], rand.Intn(1000)} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
	{Level: "DEBUG", MessageFmt: "session %s restored for user %s, %d items in cart", ArgsGen: func(s serviceSpec) []interface{} { return []interface{}{randomHex(32), usernames[rand.Intn(len(usernames))], rand.Intn(20)} }, StackGen: nil, ExtraGen: nil, CallerGen: func(s serviceSpec) string { return s.Files[rand.Intn(len(s.Files))] + fmt.Sprintf(":%d", rand.Intn(500)+1) }},
}

// ---------- 辅助数据池 ----------

var dbTables = []string{"users", "orders", "products", "payments", "sessions", "inventory", "invoices", "notifications", "audit_logs", "coupons", "shipments", "reviews", "categories", "cart_items", "refunds", "subscriptions", "api_keys", "migrations", "failed_jobs", "contacts"}
var dbColumns = []string{"id", "user_id", "email", "status", "created_at", "updated_at", "order_id", "product_id", "amount", "type"}
var dbMigrationErrors = []string{"column already exists", "foreign key constraint failed", "syntax error near REFERENCES", "duplicate index name", "table already exists"}
var upstreamServices = []string{"auth-service", "billing-api", "search-engine", "recommendation", "email-sender", "sms-gateway", "cdn-proxy", "analytics", "config-server", "feature-flags", "geo-service", "rate-limiter"}
var apiPaths = []string{"users", "orders", "products", "payments/charge", "auth/token", "notifications/send", "inventory/check", "search/query", "reports/generate", "webhooks/callback", "health", "metrics", "config/reload", "batch/process", "export/csv"}
var httpErrorCodes = []int{500, 502, 503, 504, 429, 408, 413, 422}
var httpOKCodes = []int{200, 201, 204, 301, 302, 304}
var httpErrorBodies = []string{"internal server error", `{"error":"service unavailable"}`, "upstream connect error", "gateway timeout", `{"code":"RATE_LIMITED","retry_after":60}`, "request timeout", "bad gateway: no healthy upstream"}
var httpMethods = []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
var cacheKeys = []string{"user", "product", "session", "config", "feature_flag", "rate_limit", "cart", "search_result", "recommendation", "token"}
var usernames = []string{"alice", "bob", "charlie", "dave", "eve", "frank", "grace", "heidi", "ivan", "judy", "karl", "linda", "mike", "nancy", "oscar", "peggy", "quinn", "rachel", "steve", "tina"}
var jsonTypes = []string{"string", "number", "object", "array", "bool", "null"}
var goTypes = []string{"int64", "string", "bool", "[]byte", "map[string]interface{}", "time.Time", "uuid.UUID", "float64", "*User", "*Order"}
var protoFields = []string{"user_id", "order_id", "timestamp", "amount", "status", "payload", "session_id"}
var protoMessages = []string{"CreateOrderRequest", "UserProfile", "PaymentEvent", "NotificationPayload", "InventoryUpdate", "SearchQuery"}
var goPanicMsgs = []string{"interface conversion: interface is nil, not string", "slice bounds out of range", "makeslice: len out of range", "concurrent map writes", "stack overflow"}
var goOperations = []string{"db.Query", "http.Do", "redis.Get", "grpc.Invoke", "s3.Upload", "kafka.Produce", "dns.Resolve"}
var filePaths = []string{"/var/log/app.log", "/tmp/upload_cache", "/data/exports/report.csv", "/var/run/app.pid", "/etc/app/config.yaml"}
var diskMounts = []string{"/", "/data", "/var/log", "/tmp", "/mnt/storage"}
var amqpErrors = []string{"CHANNEL_ERROR - expected 'channel.open'", "CONNECTION_FORCED - broker forced connection closure", "PRECONDITION_FAILED - queue 'events' in vhost '/' not found", "RESOURCE_LOCKED - cannot obtain exclusive access to queue"}
var mqQueues = []string{"order.created", "payment.processed", "email.send", "notification.push", "inventory.sync", "report.generate", "audit.log", "user.registered"}
var cronJobs = []string{"cleanup-expired-sessions", "sync-inventory", "generate-daily-report", "flush-cache", "rotate-api-keys", "archive-old-orders", "update-search-index", "check-subscription-renewals"}
var grpcServices = []string{"UserService", "OrderService", "PaymentService", "NotificationService", "InventoryService", "SearchService"}
var grpcMethods = []string{"GetUser", "CreateOrder", "ProcessPayment", "SendNotification", "CheckStock", "Search", "BatchUpdate", "StreamEvents"}

// PHP specific
var phpTypes = []string{"string", "int", "float", "array", "bool", "object", "callable"}
var phpProperties = []string{"connection", "logger", "cache", "config", "request", "response", "session", "user", "repository", "entityManager"}
var phpArrayKeys = []string{"user_id", "email", "password", "token", "status", "type", "config", "data", "items", "meta"}
var phpSyntaxTokens = []string{"}", ";", ")", ",", "=>", "->", "::", "$", "use"}
var phpExpectTokens = []string{"{", "(", ";", "variable", "function", "class", "string", "identifier"}
var phpClassSuffixes = []string{"Factory", "Repository", "Service", "Manager", "Handler", "Listener", "Middleware", "Provider", "Resolver", "Transformer"}
var phpFilePaths = []string{"/var/www/storage/logs/laravel.log", "/tmp/php_sessions/sess_abc123", "/var/www/storage/framework/cache/data", "/var/www/bootstrap/cache/config.php"}
var artisanCommands = []string{"migrate:fresh", "cache:clear", "queue:restart", "horizon:snapshot", "schedule:run", "optimize:clear", "db:seed", "event:generate"}
var phpQueueJobs = []string{"SendWelcomeEmail", "ProcessPayment", "GenerateInvoice", "SyncInventory", "UpdateSearchIndex", "NotifyAdmin", "CleanupExpiredTokens", "ResizeImage"}

func randomIP() string {
	return fmt.Sprintf("10.%d.%d.%d", rand.Intn(255), rand.Intn(255), rand.Intn(255)+1)
}

func randomHex(n int) string {
	b := make([]byte, n/2)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// ---------- 日志条目生成 ----------

type logEntry struct {
	Timestamp  string                 `json:"timestamp"`
	Level      string                 `json:"level"`
	Service    string                 `json:"service"`
	Message    string                 `json:"message"`
	TraceID    string                 `json:"trace_id,omitempty"`
	Caller     string                 `json:"caller,omitempty"`
	StackTrace string                 `json:"stack_trace,omitempty"`
	Extra      map[string]interface{} `json:"extra,omitempty"`
}

func generateEntry(svc serviceSpec, baseTime time.Time, seq int) logEntry {
	var templates []errorTemplate
	if svc.Lang == "go" {
		templates = goErrorMessages
	} else {
		templates = phpErrorMessages
	}

	tmpl := templates[rand.Intn(len(templates))]

	// 时间戳: baseTime + 随机偏移，保持大致递增
	ts := baseTime.Add(time.Duration(seq)*time.Millisecond*50 + time.Duration(rand.Intn(30000))*time.Millisecond)

	var msg string
	if tmpl.ArgsGen != nil {
		args := tmpl.ArgsGen(svc)
		if args != nil {
			msg = fmt.Sprintf(tmpl.MessageFmt, args...)
		} else {
			msg = tmpl.MessageFmt
		}
	} else {
		msg = tmpl.MessageFmt
	}

	entry := logEntry{
		Timestamp: ts.Format("2006-01-02T15:04:05.000-07:00"),
		Level:     tmpl.Level,
		Service:   svc.Name,
		Message:   msg,
	}

	// trace_id: ~70% 的概率生成
	if rand.Float64() < 0.7 {
		entry.TraceID = randomHex(24)
	}

	// caller
	if tmpl.CallerGen != nil {
		entry.Caller = tmpl.CallerGen(svc)
	}

	// stack_trace: 仅 ERROR/FATAL
	if (tmpl.Level == "ERROR" || tmpl.Level == "FATAL") && tmpl.StackGen != nil {
		entry.StackTrace = tmpl.StackGen(svc)
	}

	// extra
	if tmpl.ExtraGen != nil {
		entry.Extra = tmpl.ExtraGen(svc)
	}

	return entry
}

// ---------- main ----------

func main() {
	dir := flag.String("dir", "/tmp/argus-logs", "output directory for log files")
	count := flag.Int("count", 20000, "number of entries per file")
	appendMode := flag.Bool("append", false, "append to existing files instead of overwriting")
	flag.Parse()

	if err := os.MkdirAll(*dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create directory %s: %v\n", *dir, err)
		os.Exit(1)
	}

	baseTime := time.Now().Add(-30 * time.Minute)

	for i, svc := range services {
		filename := filepath.Join(*dir, fmt.Sprintf("%s.log", svc.Name))
		var f *os.File
		var err error
		if *appendMode {
			f, err = os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		} else {
			f, err = os.Create(filename)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open %s: %v\n", filename, err)
			os.Exit(1)
		}

		enc := json.NewEncoder(f)
		enc.SetEscapeHTML(false)

		for j := 0; j < *count; j++ {
			entry := generateEntry(svc, baseTime, j)
			if err := enc.Encode(entry); err != nil {
				fmt.Fprintf(os.Stderr, "failed to encode entry: %v\n", err)
				f.Close()
				os.Exit(1)
			}
		}

		f.Close()
		fmt.Printf("[%d/10] %-25s → %s (%d entries)\n", i+1, svc.Name, filename, *count)
	}

	fmt.Println("\nDone! Generated 10 files × 20000 entries = 200000 log entries total.")
}

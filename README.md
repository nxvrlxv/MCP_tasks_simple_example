# MCP-сервер на Go: памятка

Конспект по первому MCP-серверу на официальном Go SDK. Сервер написан как мок таск-трекера для кейса Avito Start (AI-buddy для стажёров): агент через MCP получает и добавляет онбординг-задачи стажёров.

Что сделано:

- поднят MCP-сервер на `github.com/modelcontextprotocol/go-sdk` с транспортом Streamable HTTP;
- реализованы три tool: `list_intern_tasks`, `add_intern_task`, `list_interns`;
- хранилище вынесено в `TaskStore` с `sync.RWMutex`, хендлеры получают его через замыкания;
- сервер проверен через MCP Inspector и связку mcphost + Ollama.

---

## 1. Как устроен MCP

### Роли

| Участник | Что делает |
|---|---|
| **MCP-сервер** (этот проект) | выставляет tools и выполняет их |
| **Хост с MCP-клиентом** (mcphost, Claude Desktop, свой агент) | подключается к серверу, передаёт модели список tools, исполняет вызовы, возвращает результаты |
| **Модель** | решает, какой tool вызвать и с какими аргументами, формулирует ответ |

Модель **не является клиентом**: она только генерирует текст, в том числе JSON вида «хочу вызвать tool X с аргументами Y». Все сетевые вызовы делает хост.

### Цикл агента (что делает хост)

1. `tools/list` — получить у сервера имена, описания и JSON-схемы tools.
2. Перевести их в формат tool calling конкретной модели (поле `tools` у Ollama и OpenAI-совместимых API).
3. Отправить модели сообщение пользователя + список tools.
4. Модель отвечает текстом (конец) или запросом вызова tool.
5. Хост вызывает `CallTool` на сервере.
6. Результат добавляется в историю с ролью `tool`, переход к шагу 3.
7. Обязателен лимит шагов: маленькие модели зацикливаются.

Ошибку tool нужно отдавать модели, а не обрывать цикл: тогда модель сможет переспросить пользователя.

### Что видит модель

| Что | Модель | Код |
|---|---|---|
| `Description` tool | да | да |
| описания полей входа (`inputSchema`) | да | да |
| описания полей выхода (`outputSchema`) | **обычно нет** | да |
| `Content` результата | да | да |

В форматах tool calling у моделей нет поля для схемы результата, поэтому смысл полей ответа объясняется в `Description` tool, понятными именами полей или текстом в `Content`.

---

## 2. Транспорты

### stdio

- Сервер читает JSON-RPC из stdin и пишет в stdout.
- Клиент сам запускает сервер как дочерний процесс (`mcp.CommandTransport`).
- **В stdout сервера нельзя писать ничего, кроме протокола.** `fmt.Println` ломает обмен, `log` по умолчанию пишет в stderr и безопасен.
- Хост и сервер должны жить на одной машине (в одном контейнере).

### Streamable HTTP

- Один эндпоинт, обычно `/mcp`.
  - **POST** — клиент отправляет JSON-RPC сообщение.
  - **GET** — клиент открывает поток для сообщений, инициированных сервером.
  - **DELETE** — завершение сессии (если сессии используются).
- Ответ на POST бывает двух видов:
  - `application/json` — один запрос, один ответ;
  - `text/event-stream` (SSE) — поток событий: прогресс, уведомления, затем результат.
- Клиент обязан слать `Accept: application/json, text/event-stream`.
- Заменил старый транспорт HTTP+SSE с двумя эндпоинтами (`/sse` и `/messages`).
- Каждый MCP-сервер становится отдельным сервисом в `docker-compose`, агент ходит по URL вида `http://tracker:8080/mcp`.

**SSE** — односторонний поток сервер → клиент поверх обычного HTTP: ответ не закрывается, сервер дописывает события `data: {...}`. В отличие от WebSocket, без проблем проходит через прокси.

### Сессии и stateless

- Классический MCP сессионный: при подключении `initialize` с версией протокола, capabilities и `clientInfo`/`serverInfo`, сервер выдаёт `Mcp-Session-Id` и хранит состояние. За балансировщиком это ломается.
- Ревизия протокола **2026-07-28** движется к stateless: версия протокола, информация о клиенте и capabilities передаются в `_meta` каждого запроса; добавлен RPC `server/discover`, SDK при неудаче откатывается к `initialize`.

---

## 3. Шпаргалка по пакету `mcp`

Импорт: `github.com/modelcontextprotocol/go-sdk/mcp`. Точные сигнатуры и поля сверять через `go doc github.com/modelcontextprotocol/go-sdk/mcp <Имя>`.

### Сервер

```go
server := mcp.NewServer(&mcp.Implementation{
	Name:    "avito-buddy-tracker", // стабильный уникальный идентификатор
	Title:   "Таск-трекер (мок)",   // для отображения в UI хоста
	Version: "v0.1.0",              // версия ЭТОГО приложения, не протокола и не SDK
}, nil)
```

`Implementation` — «паспорт» программы, уходит собеседнику как `serverInfo`/`clientInfo`. Нужен для логов, отладки, обходов совместимости и UI. Согласование возможностей держится на версии протокола и capabilities, а не на нём. Секреты туда не класть.

### Регистрация tool

```go
mcp.AddTool(server, &mcp.Tool{
	Name:        "list_intern_tasks",
	Description: "Что делает tool и когда его вызывать",
}, handler)
```

- `mcp.AddTool` — **функция**, а не метод: она дженериковая по `In` и `Out`, а у методов в Go не бывает собственных параметров типа.
- Схемы входа и выхода генерируются из структур `In` и `Out` автоматически.
- `server.AddTool(tool, rawHandler)` — низкоуровневый метод: хендлер без типизированного входа, аргументы разбираются вручную, схема задаётся в `Tool.InputSchema`.

### Сигнатура хендлера

```go
type ToolHandlerFor[In, Out any] func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)
```

- Это **функциональный тип**, а не интерфейс. Функция подходит, если совпадает сигнатура; имена параметров не важны.
- `In` и `Out` выводятся компилятором из сигнатуры переданной функции.
- **Вход ровно один.** Все аргументы tool собираются в одну структуру. Функция с двумя входными структурами не скомпилируется.
- Возвращаемых значений три, без них функция не подходит под тип.

### `CallToolRequest`

```go
type CallToolRequest = ServerRequest[*CallToolParamsRaw]
```

- `ServerRequest[P]` — дженерик-структура (сессия + параметры + данные транспорта), `CallToolRequest` — её **алиас** (знак `=`), а не новый тип, поэтому все методы сохраняются.
- `req.Params.Arguments` — сырые аргументы (`json.RawMessage`), в типизированном хендлере уже разобраны в `In`.
- `req.Session` — сессия клиента (логирование, прогресс, уведомления).
- `req.Extra` — данные транспорта, например заголовки HTTP (пригодится для авторизации).
- Передаётся по указателю по той же логике, что `*http.Request`: объект с идентичностью, без копирования.

### `CallToolResult`

| Поле | Для кого | Что внутри |
|---|---|---|
| `Content` | для модели | блоки: `*mcp.TextContent`, `*mcp.ImageContent`, ссылки на ресурсы |
| `StructuredContent` | для кода | JSON по `OutputSchema` |
| `IsError` | для обоих | tool отработал с ошибкой |

- Вернуть `nil` — SDK соберёт результат из `Out` сам: структура в `StructuredContent`, тот же JSON текстом в `Content`.
- Свой результат нужен, когда модели лучше отдать человеческий текст, когда контент не текстовый (картинка) или для особой ошибки.
- `Out` как `any` — схема выхода не генерируется, результат собирается вручную.
- `ImageContent.Data` — `[]byte`, `encoding/json` кодирует его в base64 сам.
- Ошибка из хендлера уходит модели как tool error с `IsError`, а не как протокольная. При ошибке `Out` отбрасывается, дублировать в нём сообщение незачем.

### Транспорты на сервере

```go
// stdio
err := server.Run(ctx, &mcp.StdioTransport{})

// Streamable HTTP: возвращает http.Handler
h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
```

- Первый аргумент `NewStreamableHTTPHandler` — фабрика: для каждого запроса решает, какой MCP-сервер его обслуживает. Можно отдавать разные наборы tools по роли из токена; `nil` — отказ.
- Второй аргумент — `*mcp.StreamableHTTPOptions` (stateless-режим, только JSON без SSE и т.п.).

### Клиент

```go
client := mcp.NewClient(&mcp.Implementation{Name: "mcp-client", Version: "v0.1.0"}, nil)

// stdio: клиент запускает сервер сам
transport := &mcp.CommandTransport{Command: exec.Command("./myserver")}
// HTTP:
// transport := &mcp.StreamableClientTransport{Endpoint: "http://localhost:8080/mcp"}

session, err := client.Connect(ctx, transport, nil)
defer session.Close()

tools, err := session.ListTools(ctx, nil)
res, err := session.CallTool(ctx, &mcp.CallToolParams{
	Name:      "greet",
	Arguments: map[string]any{"name": "you"},
})
```

Контент результата разбирать через type switch, а не через `c.(*mcp.TextContent)`: при другом типе будет паника.

### In-memory транспорт для тестов

```go
serverT, clientT := mcp.NewInMemoryTransports()
_, err := server.Connect(ctx, serverT, nil)
session, err := client.Connect(ctx, clientT, nil)
```

Клиент и сервер в одном процессе, без сети и дочерних процессов. Проверяет весь путь: регистрацию, схемы, сериализацию, вызов хендлера.

---

## 4. JSON-схема из структур

Схему строит пакет `github.com/google/jsonschema-go`.

```go
type ListTasksInput struct {
	InternID string `json:"intern_id" jsonschema:"ID стажёра, который задаёт вопрос; по нему ищутся его задачи"`
	Status   string `json:"status,omitempty" jsonschema:"фильтр: TODO, IN_PROGRESS или DONE; не указывай, если нужны все"`
}
```

- Значение тега `jsonschema` целиком становится описанием поля. Синтаксис `jsonschema:"description=..."` из других библиотек здесь не используется.
- Поле без `omitempty`/`omitzero` попадает в `required`. Если опциональный параметр сделать обязательным, модель начнёт выдумывать значение.
- **Имя поля и описание должны говорить одно и то же.** Поле `name` с описанием про ID — модель передаст туда имя, а не ID.
- Tool без аргументов: вход — пустая структура `type ListInternsInput struct{}`.

---

## 5. Как проектировать tools для модели

Выводы из статьи «Пишем свой MCP-сервер на Go» (Хабр, Cloud.ru) и собственных экспериментов:

1. **Description важнее имени.** Модель видит только имя, описание и схему. Описание отвечает на «что делает» и «когда вызывать».
2. **Однозначные поля и значения.** `delivery_date` путает модель, `expected_delivery_date` + `delivered_at` — нет. Статусы лучше описывают текущее состояние (`IN_PROGRESS`), а не прошедшее событие (`STARTED`).
3. **Минимум данных.** Обычный API отдаёт максимум, модели нужно только то, что пригодится для ответа. Длинные ответы съедают контекст маленьких моделей.
4. **Ошибки в теле ответа, понятным текстом.** Указывать полученное значение и допустимые варианты: модель исправит вызов с первой попытки.
5. **Связи между tools — в описании.** «За подробным списком задач вызывай `list_intern_tasks`» — без этого маленькая модель не сделает второй вызов.
6. **Модель не знает того, чего не дают tools.** Без `list_interns` запрос «задачи всех стажёров» невыполним: модель либо спросит ID, либо начнёт угадывать, либо сочинит ответ.
7. **Гранулярные tools + сводка** обычно лучше одного «отдай всё»: сводка с прогрессом, детали по запросу.

---

## 6. Код проекта: ключевые решения

### Хранилище с мьютексом

```go
type TaskStore struct {
	mu    sync.RWMutex
	tasks map[string][]Task
}

func (s *TaskStore) List(internID string) ([]Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tasks, ok := s.tasks[internID]
	return slices.Clone(tasks), ok
}

func (s *TaskStore) Add(internID string, t Task) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[internID] = append(s.tasks[internID], t)
	return len(s.tasks[internID])
}
```

- HTTP-сервер обрабатывает запросы в разных горутинах → параллельные вызовы tools → без мьютекса `fatal error: concurrent map writes`. Проверка: `go run -race .`.
- `RWMutex`: чтений больше, чем записей, `RLock` пускает их параллельно.
- `slices.Clone`: без копии наружу уходит слайс над тем же массивом, и параллельный `append` даёт неочевидную гонку.
- Порядок обхода map в Go случайный — результат сортируется (`slices.SortFunc`), иначе ответ «прыгает» между вызовами.

### Хендлеры через замыкания

```go
func NewAddTaskHandler(store *TaskStore) mcp.ToolHandlerFor[AddTaskInput, AddTaskOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in AddTaskInput) (*mcp.CallToolResult, AddTaskOutput, error) {
		// валидация → store.Add → результат
	}
}

mcp.AddTool(server, &mcp.Tool{Name: "add_intern_task", Description: "..."}, NewAddTaskHandler(store))
```

Зависимости передаются явно, без глобальных переменных. При переходе с map на Postgres меняется только реализация хранилища, хендлеры остаются прежними — та же слоистая схема handler → service → repository.

### HTTP-обвязка

```go
mux := http.NewServeMux()
mux.Handle("/mcp", logRequests(mcpHandler))
mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

srv := &http.Server{
	Addr:              ":8080",
	Handler:           mux,
	ReadHeaderTimeout: 5 * time.Second,
	// WriteTimeout не задавать: обрывает SSE-потоки
}
```

### Tools проекта

| Tool | Вход | Что делает |
|---|---|---|
| `list_intern_tasks` | `intern_id`, опционально `status` | задачи стажёра с фильтром по статусу, человекочитаемый текст в `Content` |
| `add_intern_task` | `intern_id`, название, наставник, опционально `status` (по умолчанию `TODO`) | добавляет задачу, возвращает её и общее число задач |
| `list_interns` | — | все стажёры со сводкой: всего, выполнено, в работе |

Тестовые данные: стажёры 42–45 в разной стадии онбординга, 46 — всё выполнено, 47 — существует без задач (отличие «задач нет» от «стажёр не найден», например ID 99).

---

## 7. Go-концепции, повторённые по ходу

### Дженерики

- Параметры типа: `func Map[T, U any](s []T, f func(T) U) []U`.
- Ограничения — интерфейсы как множества типов: `~int | ~int64 | ~float64`.
  - `~T` — любой тип с базовым типом `T` (пропускает `type Rubles int64`).
  - `any`, `comparable` (для `==` и ключей map), `cmp.Ordered` (для `<`).
- Внутри функции с `T` можно делать только то, что разрешено для всех типов ограничения.
- Вывод типов из аргументов; явно указывать, если параметр не встречается в аргументах (`New[int]()`).
- Нулевое значение: `var zero T` (вернуть `nil` для `T` нельзя).
- **У методов не бывает собственных параметров типа** → такие вещи пишутся свободными функциями (`mcp.AddTool`).
- Нет специализации; обход — `switch any(v).(type)`, но это сигнал, что дженерик лишний.
- Реализация: **GC shape stenciling**. Код генерируется под «форму» типа в памяти, все указатели делят одну копию, различия передаются через скрытый словарь. Для value-типов быстро, для указателей и интерфейсов вызовы методов `T` идут через словарь и хуже инлайнятся.
- Дженерики — когда одинаковый код для разных типов с сохранением типа; интерфейсы — когда разное поведение.

### Алиас и новый тип

```go
type A = Box[int] // алиас: тот же тип, методы на месте
type B Box[int]   // новый тип: методы теряются, нужна конверсия
```

Алиас на инстанцированный дженерик — с Go 1.18; алиасы со своими параметрами (`type A[T any] = Box[T]`) — с Go 1.24.

### Функциональные типы и `http.HandlerFunc`

- У обычной функции методов нет. В `net/http` функция приводится к типу `HandlerFunc`, у которого есть метод `ServeHTTP`, и уже он реализует интерфейс `Handler` (паттерн адаптера).
- В MCP SDK шаг только один: сигнатура совпадает с функциональным типом `ToolHandlerFor`, интерфейса нет.

### Грабли из практики

- **Два `package main` с `main()` в одной папке** — `main redeclared in this block`. Структура: `cmd/server/main.go`, `cmd/client/main.go`.
- **`exec.Command("myserver")`** без слеша ищет бинарь только в `$PATH`. С Go 1.19 запуск из текущей папки без `./` запрещён (`exec.ErrDot`). Нужно `./myserver`, путь считается от рабочей директории процесса (в дебаггере IDE она может отличаться).
- **`log.Fatal` вызывает `os.Exit`** — `defer` не выполнятся.
- **Строки ошибок** — со строчной буквы, без точки в конце (проверяет `staticcheck`).
- **`go run` упал, а Inspector показывает сервер** — значит, висит старый процесс на порту. Перезапустить и заново нажать List Tools.

---

## 8. Как проверять

### Запуск сервера

```bash
go run .
# MCP server on :8080/mcp
```

### MCP Inspector (модель не нужна, нужен Node.js)

```bash
npx @modelcontextprotocol/inspector
```

Transport Type: Streamable HTTP, URL `http://localhost:8080/mcp`, Server ID — любое локальное имя подключения. Tools → List Tools → вызвать с аргументами. Видны схемы, описания и сырые ответы.

Для stdio-сервера: `npx @modelcontextprotocol/inspector ./myserver`.

### Ollama + mcphost (проверка с моделью)

```bash
ollama pull qwen3:4b                          # 1.7b тоже работает, но хуже выбирает tools
go install github.com/mark3labs/mcphost@latest # бинарь в $(go env GOPATH)/bin
```

`~/.mcphost.yml`:

```yaml
mcpServers:
  tracker:
    url: "http://localhost:8080/mcp"
    transport: streamable

model: "ollama:qwen3:4b"
debug: true
```

Запуск: `mcphost`. Если модель игнорирует tool: в debug-выводе проверить, дошёл ли он до модели; если дошёл — улучшить описание или взять модель крупнее.

### curl (посмотреть транспорт вживую)

```bash
curl -i -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

Смотреть на `Content-Type` ответа и заголовок `Mcp-Session-Id`. Без `Accept` с обоими типами сервер вернёт ошибку.

### Тесты

```bash
go test ./... -v -race
```

---

## 9. Дальше

1. Postgres вместо map: `TaskStore` → интерфейс репозитория, pgx, golang-migrate, `docker-compose` с healthcheck на `/healthz`.
2. Второй MCP-сервер — мок вики с `search_docs` на полнотекстовом поиске Postgres (`tsvector`, `ts_rank`, GIN-индекс, русская морфология).
3. Тесты на все tools через in-memory транспорт + `-race`.
4. Свой минимальный агент на Go поверх Ollama — понять хост-сторону целиком.
5. Остальные возможности MCP: resources, prompts, аннотации tools (read-only / изменяющий данные).
6. Разграничение доступа: стажёр видит только свои задачи, `list_interns` — только наставнику (через фабрику в `NewStreamableHTTPHandler`).

---

## Ссылки

- [Официальный Go SDK](https://github.com/modelcontextprotocol/go-sdk) и [релизы](https://github.com/modelcontextprotocol/go-sdk/releases)
- [Статья «Пишем свой MCP-сервер на Go» (Хабр, Cloud.ru)](https://habr.com/ru/companies/cloud_ru/articles/935390/)
- [MCP Inspector](https://github.com/modelcontextprotocol/inspector)
- [mcphost](https://github.com/mark3labs/mcphost)
- [Ollama](https://ollama.com/download)

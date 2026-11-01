package main

import (
	"context"
	"fmt"
	"log"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Task struct {
	Name   string
	Status string
	Mentor string
}

type TaskInternInput struct {
	InternID string `json:"name" jsonschema:"id стажера который спрашивает вопрос и для которого нужно искать список задач"`
}

type TaskStore struct {
	mu    *sync.RWMutex
	Tasks map[string][]Task `json:"tasks"`
}

type AddTaskInput struct {
	InternID string `json:"id" jsonschema:"id стажера, которому нужно добавить задачу"`
	Name     string `json:"name" jsonschema:"Имя новой задачи, которую нужно добавить"`
	Mentor   string `json:"mentor" jsonschema:"имя ментора, кому сдавать задачу"`
	Status   string `json:"status,omitempty" jsonschema:"Статус задачи начальный статус: TODO, IN_PROGRESS или DONE; если не указан, будет TODO"`
}

type AddTaskOutput struct {
	Task       Task `json:"task" jsonschema:"Добавленная задача"`
	TotalTasks int  `json:"total_tasks" jsonschema:"Итоговое число задач у стажера"`
}

type ListAllTasksOfInternsInput struct {
	AdminID string `json:"admin_id" jsonschema:"id администратора, обычный стажер не может смотреть задачи других стажеров"`
}

type ListOFInternsOutput struct {
	Interns []string `json:"interns" jsonschema:"Список ID всех стажеров из базы"`
}

type ListTaskOutput struct {
	Tasks []Task `json:"tasks" jsonschema:"Список задач конкретного стажера"`
}

var tasksByIntern = map[string][]Task{
	// Бэкенд-стажёр, середина онбординга
	"42": {
		{Name: "Получить доступы: GitLab, VPN, корпоративная почта", Status: "DONE", Mentor: "Анна"},
		{Name: "Настроить локальное окружение", Status: "DONE", Mentor: "Анна"},
		{Name: "Прочитать архитектурный обзор сервиса объявлений", Status: "DONE", Mentor: "Анна"},
		{Name: "Разобраться в сервисе объявлений", Status: "IN_PROGRESS", Mentor: "Анна"},
		{Name: "Поднять сервис локально через docker-compose", Status: "IN_PROGRESS", Mentor: "Анна"},
		{Name: "Изучить код-стайл и линтеры команды", Status: "TODO", Mentor: "Олег"},
		{Name: "Сделать первый code review", Status: "TODO", Mentor: "Олег"},
		{Name: "Закрыть первую задачу из бэклога", Status: "TODO", Mentor: "Анна"},
	},

	// Новичок, только вышел
	"43": {
		{Name: "Получить доступы: GitLab, VPN, корпоративная почта", Status: "IN_PROGRESS", Mentor: "Дмитрий"},
		{Name: "Познакомиться с командой на дейлике", Status: "TODO", Mentor: "Дмитрий"},
		{Name: "Настроить локальное окружение", Status: "TODO", Mentor: "Дмитрий"},
		{Name: "Прочитать гайд по процессу релизов", Status: "TODO", Mentor: "Мария"},
		{Name: "Разобраться в схеме базы данных сервиса платежей", Status: "TODO", Mentor: "Мария"},
	},

	// Почти закончил онбординг
	"44": {
		{Name: "Получить доступы: GitLab, VPN, корпоративная почта", Status: "DONE", Mentor: "Игорь"},
		{Name: "Настроить локальное окружение", Status: "DONE", Mentor: "Игорь"},
		{Name: "Изучить мониторинг сервиса в Grafana", Status: "DONE", Mentor: "Игорь"},
		{Name: "Написать первый интеграционный тест", Status: "DONE", Mentor: "Игорь"},
		{Name: "Выкатить первый мердж-реквест в прод", Status: "IN_PROGRESS", Mentor: "Игорь"},
		{Name: "Провести демо своей задачи для команды", Status: "TODO", Mentor: "Елена"},
	},

	// Фронтенд-стажёр
	"45": {
		{Name: "Получить доступы: GitLab, Figma, корпоративная почта", Status: "DONE", Mentor: "Ксения"},
		{Name: "Изучить дизайн-систему компании", Status: "IN_PROGRESS", Mentor: "Ксения"},
		{Name: "Разобраться в структуре React-приложения", Status: "IN_PROGRESS", Mentor: "Ксения"},
		{Name: "Сверстать компонент карточки объявления", Status: "TODO", Mentor: "Ксения"},
		{Name: "Покрыть компонент тестами", Status: "TODO", Mentor: "Павел"},
	},

	// Онбординг завершён: все задачи выполнены
	"46": {
		{Name: "Получить доступы: GitLab, VPN, корпоративная почта", Status: "DONE", Mentor: "Олег"},
		{Name: "Настроить локальное окружение", Status: "DONE", Mentor: "Олег"},
		{Name: "Сделать первый code review", Status: "DONE", Mentor: "Олег"},
		{Name: "Закрыть первую задачу из бэклога", Status: "DONE", Mentor: "Олег"},
	},

	// Стажёр заведён, но задач ещё нет
	"47": {},
}

var validStatuses = map[string]bool{"TODO": true, "IN_PROGRESS": true, "DONE": true}

func NewTaskStore(tasks map[string][]Task) *TaskStore {
	var mux sync.RWMutex
	return &TaskStore{
		mu:    &mux,
		Tasks: tasks,
	}
}

func (ts *TaskStore) GetTasks(id string) ([]Task, error) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	tasks, ok := ts.Tasks[id]
	if !ok {
		return []Task{}, fmt.Errorf("Not Found tasks for this id")
	}

	return tasks, nil
}

func (ts *TaskStore) AddTask(newTask Task, UserID string) int {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	ts.Tasks[UserID] = append(ts.Tasks[UserID], newTask)
	return len(ts.Tasks[UserID])
}

func (ts *TaskStore) ListOfInterns(tasks map[string][]Task) []string {
	return slices.Collect(maps.Keys(tasks))
}

func ListTasks(ts *TaskStore) func(ctx context.Context, req *mcp.CallToolRequest, input TaskInternInput) (*mcp.CallToolResult, ListTaskOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input TaskInternInput) (*mcp.CallToolResult, ListTaskOutput, error) {
		tasks, err := ts.GetTasks(input.InternID)
		if err != nil {
			return nil, ListTaskOutput{}, fmt.Errorf("стажёр с ID %s не найден", input.InternID)
		}
		return nil, ListTaskOutput{Tasks: tasks}, nil
	}
}

func AddTask(ts *TaskStore) func(ctx context.Context, req *mcp.CallToolRequest, inputTask AddTaskInput) (*mcp.CallToolResult, AddTaskOutput, error) {

	return func(ctx context.Context, req *mcp.CallToolRequest, inputTask AddTaskInput) (*mcp.CallToolResult, AddTaskOutput, error) {
		task := Task{
			Name:   inputTask.Name,
			Status: inputTask.Status,
			Mentor: inputTask.Mentor,
		}
		countTasks := ts.AddTask(task, inputTask.InternID)
		return nil, AddTaskOutput{Task: task, TotalTasks: countTasks}, nil
	}
}

func ListOfInternsW(ts *TaskStore) func(ctx context.Context, req *mcp.CallToolRequest, inputTask ListAllTasksOfInternsInput) (*mcp.CallToolResult, ListOFInternsOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, inputTask ListAllTasksOfInternsInput) (*mcp.CallToolResult, ListOFInternsOutput, error) {
		if inputTask.AdminID != "zalupa" {
			return nil, ListOFInternsOutput{}, fmt.Errorf("Ошибка: доступ к данной фунции есть только у администратора")
		}
		InternsIDs := ts.ListOfInterns(ts.Tasks)
		return nil, ListOFInternsOutput{Interns: InternsIDs}, nil
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:        "avito-buddy-tracker",
		Title:       "Таск-трекер",
		Description: "Микро сервачок, тест MCP протокола, супер просто таск трекер",
		Version:     "v0.1.0",
	}, nil)
	ts := NewTaskStore(tasksByIntern)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_intern_tasks",
		Description: "Функция, которая позволяет смотреть список задач стажера по его id. Используй ее, когда стажер спрашивает про то, чем можно заняться или что по планам\n" + "Статусы: TODO — не начата, IN_PROGRESS — в работе, DONE — выполнена.",
	}, ListTasks(ts))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_task_for_intern",
		Description: "Функция, которая позволяет добавить стажеру задачку для выполнения в уже существующий(или нет) пул его задач. Используй, когда стажер говорит, что нужно поставить новую задачу",
	}, AddTask(ts))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_of_interns_ids",
		Description: "Функция позволяет взять список id всех стажеров, которые есть в базе. Используй, когда нужно найти информацию по всем задачам всех стажеров, после используй list_intern_tasks для каждого из списка, чтобы вывести все задачки по стажерам",
	}, ListOfInternsW(ts))

	mcphandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return server }, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", logRequests(mcphandler))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// WriteTimeout не задан намеренно: он обрывает SSE-потоки.
	}

	log.Println("MCP server on :8080/mcp")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

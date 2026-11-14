# schemigo

Декларативный инструмент управления миграциями PostgreSQL / MySQL / ClickHouse. Вы описываете **желаемое** состояние схемы, а schemigo вычисляет разницу с **текущим** состоянием БД и генерирует SQL для её достижения.

## Возможности

- **Декларативный подход** — описываете схему как код (Go-структуры), а не пишете миграции вручную.
- **Полный diff** — сравнивает расширения, таблицы, колонки, индексы, внешние ключи, CHECK-ограничения, функции, триггеры, представления и enum-типы
- **Безопасность** — показывает SQL перед применением, поддерживает dry-run, редактирование миграций перед запуском (если миграции редактируются нужно и проверять go структуры).
- **RENAME detection** — определяет переименование таблиц и колонок (вместо DROP + CREATE)
- **Идемпотентность** — все сгенерированные SQL-операторы используют `IF EXISTS` / `IF NOT EXISTS` и так далее, идемпотентно всегда.
- **Квотирование идентификаторов** — имена с заглавными буквами, спецсимволами и зарезервированные слова (`order`, `user`, ...) корректно экранируются
- **Синтаксическая подсветка** — SQL выводится с подсветкой через chroma
- **Экспорт схемы** — текущая схема БД экспортируется в Go-файлы для версионирования в репозитории

## Быстрый старт

### 1. Экспорт текущей схемы

```bash
schemigo -d "postgres://user:pass@localhost/dbname?sslmode=disable" --export 
```

Создаст директорию `./schema/` с Go-файлами, описывающими текущую схему:

```
schema/
├── extensions/
│   ├── types.go
│   └── pg_trgm.go
├── enums/
│   └── types.go
├── tables/
│   ├── types.go
│   ├── users.go
│   └── orders.go
├── functions/
│   ├── types.go
│   └── calculate_total.go
├── triggers/
│   ├── types.go
│   └── update_timestamp.go
└── views/
    ├── types.go
    └── active_users.go
```

### 2. Редактирование желаемой схемы

Схема описывается **декларативно**: вы указываете, как должна выглядеть БД, а не как к ней прийти.

```go
// schema/tables/users.go
package tables

var Users = Table{
    Name: "users",
    Columns: []Column{
        {Name: "id", Type: "int8", NotNull: true, PrimaryKey: true, Default: "nextval('users_id_seq'::regclass)"},
        {Name: "email", Type: "varchar(255)", NotNull: true},
        {Name: "name", Type: "text"},
        {Name: "role", Type: "user_role", NotNull: true, Default: "'user'"},
        {Name: "created_at", Type: "timestamptz", Default: "now()"},
    },
    Indexes: []Index{
        {Name: "idx_users_email", Columns: []string{"email"}, Unique: true},
    },
    Checks: []Check{
        {Name: "chk_users_email", Expression: "email ~ '^[^@]+@[^@]+$'"},
    },
}
```
Нужно создать константы типов для колонок (Postgres / MySQL / ClickHouse) чтобы не вводить вручную. 
Также сделать проще применение nextval('users_id_seq'::regclass) через константу или функцию где просто берется имя поля.
Возможно импорт пакетов с этим константами например пакет postgres и postgres.Int8, postgres.Nextval(column / index)
То есть должно быть просто и нативно.


### Генерация новых сущностей

```bash
schemigo table users 'id:int8,email:varchar(255),total:numeric(10,2)'
schemigo enum order_status 'pending,processing,shipped,delivered,cancelled'
schemigo view active_users ./active_users.sql
schemigo view -materialized user_totals ./user_totals.sql
```

Команды работают без DSN, создают `./schema` при необходимости и добавляют новую сущность в соответствующий `all.go`. Для view SQL копируется в `schema/views/` и встраивается при загрузке Go-схемы. Флаги `-dir` и `-schema` указываются после команды, перед именем; `-schema` применяется при создании каталога.

### 3. Применение миграций

```bash
schemigo -d "postgres://user:pass@localhost/dbname?sslmode=disable"
```

Migrator покажет сгенерированный SQL и предложит:
- `Y` — применить миграции
- `n` — отменить
- `e` — открыть редактор для ручной правки SQL перед применением

### Флаги CLI

| Флаг | Описание |
|------|----------|
| `-d` | Строка подключения (обязательный) |
| `-dir` | Директория со схемой (по умолчанию `./schema`) |
| `-schema` | Управляемая схема (по умолчанию `public`) |
| `-export` | Экспортировать текущую схему БД в Go-файлы |
| `-dry-run` | Показать миграции и выйти, не применяя |
| `-yes` | Применить без подтверждения (для CI) |
| `-history` | Показать историю применённых миграций |
| `-no-backup` | Не создавать бэкап схемы перед применением |

При любой ошибке CLI завершается с кодом 1 — можно безопасно использовать в CI-пайплайнах.

### Бэкап и восстановление

Перед каждым применением миграций **автоматически** создаётся бэкап текущей схемы в директорию `schemigo_backup/`. При успешном применении бэкап удаляется. При ошибке — сохраняется.

```bash
# Применить миграции (бэкап создаётся автоматически)
schemigo -d "postgres://user:pass@localhost/db"

# Отключить бэкап
schemigo -d "postgres://user:pass@localhost/db" -no-backup

# Восстановить схему из бэкапа
schemigo -d "postgres://user:pass@localhost/db" -dir ./schema_backup
```

При ошибке миграции выводится инструкция по восстановлению с точным путём к бэкапу.

### История миграций и блокировка

- Применённые миграции записываются в таблицу `schema_migrations` (SHA-256 хеш SQL, время, пользователь). Применение пропускается при пустом текущем diff; ранее записанный хеш не препятствует повторному применению после drift. Сама таблица истории исключена из diff.
- PostgreSQL использует advisory lock. ClickHouse Apply требует Keeper/ZooKeeper: CLI `-keeper host:port,...`, Go API `clickhouse.NewKeeperLocker`. Без координатора разрешены только чтение, экспорт и dry-run.
- Перед применением SQL валидируется через `EXPLAIN` (DDL и DO-блоки пропускаются, т.к. EXPLAIN их не поддерживает).

### ClickHouse

`clickhouse://` выбирает ClickHouse; база задаётся URL или `-schema`. Таблицы требуют Engine; OrderBy и PartitionBy задаются raw SQL. Поддерживаются колонки, skipping indexes (Type/Granularity), CHECK, SQL lambda functions, обычные и materialized views. Изменение engine/partition/primary key требует ручной миграции данных. Отдельные enum, Extensions, FK и Trigger отклоняются. Для materialized view Definition включает TO/ENGINE и AS SELECT; явные колонки и DEFINER/SQL SECURITY сохраняются при экспорте. Пересоздание собственного storage удаляет данные materialized view.

SQL UDF принадлежат всему серверу: Inspect включает их все, а diff удаляет отсутствующие в desired. Миграции разных баз используют одну очередь Keeper; все клиенты должны использовать один path (CLI: `/schemigo`) и endpoints. Координатор — доверенный, znodes создаются с world ACL. Ephemeral sequential znode и watch обеспечивают взаимное исключение участников этой очереди. Close освобождает сессию. Disconnect отменяет контекст Apply и дальнейшие SQL statements; уже запущенный запрос может завершиться после потери сессии, поэтому строгая защита от split-brain не заявляется.

ClickHouse DDL не объединяется в транзакцию; история на MergeTree пополняется только после успеха. После частичного сбоя повторный Apply начинает с нового Inspect/Diff. Снимок сохраняет только схему. Engine сохраняет SETTINGS; непредставимые TTL/SAMPLE BY и MATERIALIZED/ALIAS columns отклоняются; сравнение SQL не включает полную семантическую канонизацию. Интеграционные сценарии проходят на одноразовых ClickHouse 25.8 и ZooKeeper 3.9.3; CI запускает их отдельным обязательным check наряду с PostgreSQL 18.

## Типы сущностей

### Extension

```go
Extension{
    Name: "pg_trgm",
}

// С закреплённой версией (обычно не требуется)
Extension{
    Name:    "postgis",
    Version: "3.4.2",
}
```

Генерируется идемпотентный `CREATE EXTENSION IF NOT EXISTS`. Если `Version` задана и отличается от установленной — `ALTER EXTENSION ... UPDATE TO`. При экспорте версия не закрепляется.

**Расширения create-only**: удаление schemigo не генерирует (`DROP EXTENSION CASCADE` уничтожил бы зависимые объекты вместе с данными) — лишние расширения удаляются вручную.

### Enum

```go
Enum{
    Name:   "order_status",
    Values: []string{"pending", "processing", "shipped", "delivered", "cancelled"},
}
```

Поддерживается добавление и удаление значений. При удалении значений создаётся новый тип `_new`, затем тип переименовывается.

### Table

```go
Table{
    Name: "orders",
    Columns: []Column{
        {Name: "id", Type: "int8", NotNull: true, PrimaryKey: true},
        {Name: "user_id", Type: "int8", NotNull: true},
        {Name: "total", Type: "numeric(10,2)"},
        {Name: "status", Type: "order_status", Default: "'pending'"},
    },
    Indexes: []Index{
        {Name: "idx_orders_user_id", Columns: []string{"user_id"}},
        {Name: "idx_orders_active", Columns: []string{"user_id"}, Where: "(status = 'pending')"},
        {Name: "idx_orders_lower_email", Expression: "lower(email)"},
    },
    ForeignKeys: []ForeignKey{
        {
            Name:              "fk_orders_user_id",
            Columns:           []string{"user_id"},
            ReferencedTable:   "users",
            ReferencedColumns: []string{"id"},
            OnDelete:          "CASCADE",
        },
    },
    Checks: []Check{
        {Name: "chk_orders_total", Expression: "total >= 0"},
    },
}
```

**Column** поддерживает:
- `Using` — кастомное выражение для `ALTER TYPE ... USING`
- `PrimaryKey` — детектится при извлечении

**Index** поддерживает:
- `Method` — `btree` (по умолчанию), `hash`, `gist`, `gin`
- `Where` — частичные индексы: `"status = 'active'"`
- `Expression` — expression-индексы: `"lower(email)"`

### Function

```go
Function{
    Name:       "get_user_orders",
    Returns:    "TABLE(order_id int8, total numeric)",
    Language:   "sql",
    Parameters: "p_user_id int8",
    Body:       `SELECT id, total FROM orders WHERE user_id = p_user_id;`,
}
```

### Trigger

```go
Trigger{
    Name:       "trg_update_timestamp",
    When:       "BEFORE",
    Event:      "INSERT OR UPDATE",
    Table:      "users",
    ForEach:    "ROW",
    ForEachRow: true,
    Condition:  "OLD.name IS DISTINCT FROM NEW.name",
    Function:   "update_modified_column",
}
```

- `When`: `BEFORE`, `AFTER`, `INSTEAD OF`
- `ForEach`: `ROW` (по умолчанию), `STATEMENT`
- `Condition`: опциональное `WHEN`-условие

### View

```go
View{
    Name:         "active_users",
    Definition:   "SELECT id, email, name FROM users WHERE deleted_at IS NULL",
    Materialized: false,
}
```

При изменении определения:
- **Добавление колонок в конец** → `CREATE OR REPLACE VIEW` (быстро)
- **Удаление/перестановка колонок** → `DROP VIEW ... CASCADE` + `CREATE VIEW` (безопасно)
- **Смена типа** (view ↔ materialized view) → полное пересоздание

### Программное использование


По умолчанию: **extensions → enums → tables → functions → triggers → views**

Этот порядок можно изменить:

### Возможности

- Для новых таблиц / enum возможность генерации шаблонного кода то есть вызов schemigo -> table имя и колонки через запятую с типами, в общем главное чтобы удобно было
- Возможность использовать как CLI / так и в коде Go
- Сделать удобнее создание и редактирование VIEW 
- Сделать возможность проверки миграций без применения UP / DOWN (то есть проверяется корректность SQL и собственно миграций)

### Проблемы
 - Если редактируется миграция как обеспечить корректность схемы? Может вообще убрать редактирование
 - Как обеспечить выполнение миграций если нужно обеспечить порядок применения SQL инструкций например при изменении enum?

### Что нужно сделать
 - Интеграционные тесты на поддерживаемые БД и функционал, то есть в контейнере поднимаем БД и накатываем таблицы / измененяем колонки / индексы и соотвественно с enum, view основные сценарии

# schemigo

Спецификация реализованного поведения декларативного управления схемами PostgreSQL, MySQL и ClickHouse. Пользователь описывает желаемую схему Go-структурами; инструмент читает текущую схему, строит SQL-план и применяет разницу. Примеры и команды разработки приведены также в [README.md](README.md).

## Общий контракт

- `Schema` описывает один namespace: схему PostgreSQL или базу MySQL/ClickHouse. Объекты имеют неквалифицированные имена; драйвер квотирует идентификаторы по правилам диалекта.
- `Normalize` проверяет общую модель и возвращает независимую копию. Драйвер дополнительно проверяет возможности своего диалекта. Непредставимые свойства отклоняются, а не молча теряются.
- `Driver.Inspect` читает текущую схему; служебная таблица `schema_migrations` не входит в управляемые объекты. `Driver.Diff` строит план без применения DDL.
- `PlanMigration` выполняет Inspect/Diff. `Plan.Statements` хранит порядок, SQL и признаки `Destructive`, `SkipExplain`, `OutsideTransaction`. `Plan.SQL()` предназначен для просмотра; он не кодирует границы транзакций. `Plan.SHA256()` вычисляет SHA-256 точных байтов `SQL()`.
- `Schema.Order` задаёт предпочтения между независимыми изменениями. Порядок по умолчанию: extensions → enums → tables → functions → triggers → views; зависимости имеют приоритет.
- Повторный Apply заново читает схему и строит оставшийся diff. Произвольный старый SQL-план не гарантированно пригоден для повторного выполнения. Пустой diff не применяет SQL и не добавляет историю; ранее записанный хеш не мешает исправлению нового drift.

Типы, выражения, тела функций и определения views содержат SQL конкретного диалекта. Полной семантической канонизации SQL нет: для выражений с повторным diff следует использовать запись из Inspect. Go-схема и raw SQL должны происходить из доверенного источника.

## Поддерживаемые диалекты

| Возможность | PostgreSQL 18 | MySQL 8.4 | ClickHouse 25.8 |
| --- | --- | --- | --- |
| Таблицы, колонки, defaults, первичные ключи | Да | Да | Native Type, Engine, OrderBy, PartitionBy |
| Индексы | Columns/Expression, Unique, Method, Where | Columns/Expression, Unique, btree | Skipping indexes: Type и положительная Granularity |
| FK и CHECK | Да | Да | Только CHECK |
| Extensions и отдельные Enum | Да | Нет | Нет |
| Функции | sql/plpgsql, перегрузки | SQL-функции и процедуры без перегрузок | Серверные SQL lambda UDF |
| Триггеры | Таблицы и обычные views | ROW, BEFORE/AFTER, вызов процедуры | Нет |
| Views | Обычные и materialized | Обычные | Обычные и materialized |
| Блокировка Apply | Advisory lock | GET_LOCK | Keeper/ZooKeeper |
| Выполнение DDL | Транзакции с отдельными autocommit statements | Без общей транзакции | Без общей транзакции |

### PostgreSQL

`postgres.Open` принимает URL или keyword DSN и создаёт пул pgx. SQL-план поддерживает таблицы, колонки, составные PK/FK, CHECK, индексы, extensions, enum, функции, триггеры и views.

Extensions создаются и обновляются до явно заданной версии; лишние не удаляются. Экспорт не закрепляет версию extension. Enum поддерживает создание и добавление значений с сохранением порядка. Удаление или перестановка значений требует пересоздания типа с проверкой зависимостей через подключение к БД. Пересоздание выполняется атомарным DO-блоком; неизвестные зависимости и конфликт временного имени вызывают отказ. Комментарий сохраняется, изменение owner/ACL при пересоздании отклоняется.

Переименование таблиц/колонок определяется по единственному совпадению определения без имени; неоднозначность возвращает ошибку. Новые колонки добавляются в конец, перестановка существующих не поддерживается. `Column.Using` задаёт выражение преобразования типа. Удаления и преобразования типов отмечаются `Destructive`. Переименование колонок с raw CHECK/выражениями индексов/условиями триггеров пока отклоняется.

FK временно снимаются перед изменением опорных ключей и типов колонок и восстанавливаются после создания необходимых объектов. CHECK/FK добавляются как NOT VALID и валидируются в одном DO-блоке. DROP использует RESTRICT без CASCADE. Неизвестные зависимости вызывают отказ. Индексы строятся обычным CREATE INDEX; CONCURRENTLY не поддерживается и запись может блокироваться.

Функции идентифицируются именем и типами входных аргументов. Изменение тела использует CREATE OR REPLACE; изменение результата, имён параметров или удаление defaults может требовать пересоздания и проверки зависимостей. Тела функций сохраняются и сравниваются побайтно.

Обычные views заменяются через CREATE OR REPLACE, когда PostgreSQL подтверждает совместимость выходных колонок. Запрос описывается через pgx в read-only транзакции без выполнения SELECT. Несовместимые изменения и materialized views требуют пересоздания. Управляемые зависимые views снимаются и восстанавливаются вместе с триггерами; неизвестные зависимости, отличающийся владелец, grants, комментарии и неподдерживаемые параметры пересоздаваемых объектов вызывают отказ. Удаление materialized view отмечается `Destructive`.

Изменения существующих views и пересоздание функций/enum требуют соединения из `postgres.Open`. Зависимости функций/views выводятся консервативно по SQL-токенам: возможны ложные зависимости, динамический SQL не анализируется. Identity/generated columns, partitioning, UNIQUE constraints, INCLUDE indexes, cross-schema FK и расширенные опции функций вне модели отклоняются.

### MySQL

`mysql.Open` принимает `mysql://user:password@host:port/database`. Поддерживаются таблицы, колонки, индексы, PK/FK/CHECK, SQL-функции, процедуры, ROW-триггеры BEFORE/AFTER INSERT/UPDATE/DELETE и обычные views. Пустой `Function.Returns` обозначает процедуру; триггер вызывает процедуру без аргументов. При включённом binary log SQL-функции требуют характеристики `DETERMINISTIC`, `NO SQL` или `READS SQL DATA` в `Function.Body` перед телом.

Extensions, отдельные Enum, materialized views, partial indexes, ClickHouse-поля таблиц и `Column.Using` отклоняются. DDL выполняется в autocommit. После частичного сбоя уже выполненные команды сохраняются; следующий Apply выполняет новый Inspect/Diff. Успешная история записывается после всех команд.

### ClickHouse

`clickhouse.Open` принимает `clickhouse://host:9000/database`. Таблицы требуют `Engine`; `OrderBy` и `PartitionBy` содержат raw SQL. `Engine` сохраняет аргументы и SETTINGS. Изменение engine, partition или primary key требует ручной миграции данных. Nullable задаётся через Type, например `Nullable(String)`; первичный ключ — через `Table.PrimaryKey`. Extensions, отдельные enum, FK, triggers, unique/partial indexes, MATERIALIZED/ALIAS columns, TTL и SAMPLE BY вне модели отклоняются.

`Function.Parameters` содержит аргументы lambda, `Body` — выражение; Returns и перегрузки не поддерживаются. **SQL UDF принадлежат всему серверу:** Inspect включает все SQL UDF, и отсутствие функции в desired означает её удаление независимо от базы.

`View.Definition` содержит SELECT обычного view либо `TO db.target AS SELECT ...` / `ENGINE = MergeTree ORDER BY ... AS SELECT ...` для materialized view. Inspect сохраняет явные колонки и DEFINER/SQL SECURITY. При пересоздании существующие security clauses сохраняются, если desired не задаёт их явно. Пересоздание materialized view с собственным storage удаляет накопленные данные и отмечается `Destructive`.

Apply требует внешней блокировки. Go API: `clickhouse.NewKeeperLocker(ctx, endpoints, "/schemigo")`, затем `clickhouse.New(locker)`; вызывающий код закрывает locker. `clickhouse.New(nil)` допускает чтение, экспорт и diff, но Apply отказывает до DDL. Все миграции одного сервера, включая разные базы, должны использовать одинаковые endpoints и path. CLI использует `/schemigo`. Координатор должен находиться в доверенной сети: znodes создаются с world ACL.

Keeper использует ephemeral sequential znode и watch. Close освобождает сессию. При disconnect locker становится непригоден для повторного использования и отменяет контекст Apply, останавливая дальнейшие statements. Уже принятый сервером запрос может завершиться после потери сессии; строгая защита от split-brain не гарантируется. Пользовательский Locker должен предоставлять `MigrationContext(context.Context) (context.Context, context.CancelFunc)` для уведомления Apply о потере владения.

DDL выполняется без общей транзакции, история хранится на MergeTree и пополняется после полного успеха. После ошибки новый Apply читает фактическое состояние и строит оставшийся diff.

## Go-схема, экспорт и генерация

`schemigo.Export(ctx, db, driver, schemaName, dir)` создаёт Go-описание текущей схемы в отсутствующем или пустом каталоге внутри текущего Go-модуля. Существующие файлы не перезаписываются. Создаются `main.go` и пакеты `extensions`, `enums`, `tables`, `functions`, `triggers`, `views`; в каждом есть aliases в `types.go`, список `All` в `all.go` и отдельные файлы сущностей, например `tables/users_schema.go`.

`schemigo.Load(ctx, dir)` компилирует и запускает доверенную Go-схему с `-mod=readonly`, читает один JSON-объект из stdout и возвращает нормализованную модель. Это выполнение Go-кода, поэтому требуется установленный Go и доверие к каталогу схемы. Пул БД принадлежит вызывающему коду и закрывается им.

Пример PostgreSQL-таблицы в экспортированном пакете `tables`:

```go
var Users = Table{
    Name: "users",
    Columns: []Column{
        {Name: "id", Type: "bigint", PrimaryKey: true, NotNull: true},
        {Name: "email", Type: "character varying(255)", NotNull: true},
    },
    Indexes: []Index{
        {Name: "idx_users_email", Columns: []string{"email"}, Unique: true},
    },
}
```

При ручном добавлении сущность включается в `All`. `Table.PrimaryKey` задаёт упорядоченный составной ключ и не совмещается с `Column.PrimaryKey`. `Function` содержит Name, Parameters, Returns, Language, Body. `Trigger.ForEach` (`ROW` по умолчанию или `STATEMENT`) имеет приоритет над legacy-полем `ForEachRow`. Возможности этих полей зависят от диалекта.

Пакеты диалектов предоставляют константы типов, например `postgres.Int8`, `mysql.BigInt`, `clickhouse.UInt64`. `postgres.Nextval(table, column)` возвращает default для обычного имени `table_column_seq`, но не создаёт sequence: она должна существовать отдельно.

Генерация без подключения к БД:

```sh
schemigo table users 'id:int8,email:varchar(255),total:numeric(10,2)'
schemigo enum order_status 'pending,processing,shipped,delivered,cancelled'
schemigo view active_users ./active_users.sql
schemigo view -materialized user_totals ./user_totals.sql
```

Команды создают `./schema` при необходимости и включают сущность в соответствующий `all.go`. SQL view копируется в `schema/views/` и встраивается в Go-схему. Флаги `-dir` и `-schema` указываются после команды перед именем; `-schema` применяется при создании каталога. Повторное имя, коллизия Go-символов и некорректный ввод возвращают ошибку.

## CLI

```sh
schemigo -d 'postgres://user:pass@localhost/dbname?sslmode=disable' -export
# Отредактировать Go-схему в ./schema.
schemigo -d 'postgres://user:pass@localhost/dbname?sslmode=disable' -dry-run
schemigo -d 'postgres://user:pass@localhost/dbname?sslmode=disable' -yes
schemigo -d 'postgres://user:pass@localhost/dbname?sslmode=disable' -history
```

CLI выбирает драйвер по URL: `postgres://`, `postgresql://`, `mysql://`, `clickhouse://`. Для PostgreSQL схема по умолчанию `public`, для MySQL — база из URL, для ClickHouse — база из URL или `default`.

| Флаг | Поведение |
| --- | --- |
| `-d` | URL подключения; обязателен для работы с БД |
| `-dir` | Каталог Go-схемы, по умолчанию `./schema` |
| `-schema` | Переопределение управляемой схемы/базы |
| `-export` | Экспорт текущей схемы |
| `-dry-run` | Построить и показать SQL без применения |
| `-yes` | Применить без интерактивного подтверждения |
| `-history` | Прочитать историю, не создавая её таблицу |
| `-no-backup` | Отключить снимок схемы перед применением |
| `-keeper` | Endpoints Keeper/ZooKeeper через запятую для ClickHouse Apply |

`-export`, `-dry-run` и `-history` взаимно исключаются. Без `-yes` после SQL выводится запрос `Y/n`: только `Y`/`y` подтверждает применение, пустой ответ отменяет его. Перед применением план повторно строится под блокировкой и сверяется с SHA-256 показанного SQL; изменение плана требует нового просмотра. Редактирование SQL через CLI не поддерживается.

В терминале основные SQL-команды выделяются ANSI-цветом; pipe, файл и `NO_COLOR` отключают цвет. Ошибки завершают CLI с кодом 1, успех и отмена — с кодом 0. Export/history/dry-run ClickHouse не подключаются к Keeper.

## Применение, история и снимки

Apply получает выделенное соединение, берёт блокировку и повторяет Inspect/Diff. После проверки ожидаемого хеша и валидации команд сохраняется снимок текущих объектов в `schemigo_backup/snapshot-*`, если не задан `NoBackup`. Снимок создаётся только для непустого плана. Проверка PostgreSQL/MySQL использует EXPLAIN для подходящих statements; `SkipExplain` пропускает DDL/DO-блоки. ClickHouse проверяет DDL при выполнении. Dry-run не является полноценной проверкой исполнимости всех миграций на сервере.

Обычные PostgreSQL statements выполняются транзакционно; `OutsideTransaction` фиксирует предшествующую транзакцию и выполняется в autocommit. Добавление enum-значений требует такого режима. Обычный SQL и запись истории PostgreSQL фиксируются одной транзакцией; уже выполненные autocommit-команды сохраняются при последующей ошибке. MySQL/ClickHouse используют autocommit для DDL.

История `schema_migrations` содержит SHA-256 SQL, серверное время и пользователя БД. Запись добавляется после успешного плана; хеш не обязан быть уникальным. `History` читает журнал без создания таблицы. Снимок удаляется после успеха и сохраняется при ошибке; `ApplyError` сообщает путь и число уже зафиксированных statements.

**Снимок содержит только определения объектов, без данных.** Его можно передать через `-dir` как желаемую схему для нового планирования, но это не автоматический откат и не восстановление удалённых строк. Отдельные изменения требуют ручной миграции данных.

## Проверки и CI

Требуется Go из `go.mod` (1.27). Unit-тесты работают без БД; интеграционные файлы отделены тегом `integration` и разрешением `SCHEMIGO_TESTCONTAINERS=1`. Testcontainers создаёт одноразовые PostgreSQL 18, MySQL 8.4, ClickHouse 25.8 и ZooKeeper 3.9.3; существующие пользовательские БД для тестов не нужны.

```sh
GOTOOLCHAIN=local go test -mod=readonly -race -shuffle=on -count=1 ./...
GOTOOLCHAIN=local go vet -mod=readonly -tags=integration ./...
SCHEMIGO_TESTCONTAINERS=1 GOTOOLCHAIN=local go test -mod=readonly -race -shuffle=on -tags=integration -count=1 -timeout=10m ./...
```

CI проверяет все unit-тесты, форматирование и vet отдельно. Три независимых jobs запускают пакеты соответствующих диалектов и CLI на PostgreSQL 18, MySQL 8.4 и ClickHouse 25.8 параллельно, с `-race`, `-shuffle=on`, `-count=1`. Фильтрация в общем CLI-пакете исключает сценарии чужих БД. JSON-отчёты и покрытие сохраняются в артефактах каждого диалекта.

Отдельная матрица проверяет кросс-компиляцию всех обычных пакетов и линковку CLI для linux/amd64, windows/amd64, darwin/amd64 и darwin/arm64 с `CGO_ENABLED=0`. Запуск бинарников на Windows/macOS этими jobs не проверяется.

[Локальный шаблон ruleset](.github/rulesets/required-tests.json) перечисляет обязательные проверки. Файл не включает защиту ветки автоматически: его нужно импортировать или обновить в настройках GitHub. Наличие файла не подтверждает, что удалённое правило уже применено.

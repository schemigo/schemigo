# schemigo

Декларативное управление схемами PostgreSQL, MySQL и ClickHouse.

Реализованы общая модель схемы, `Normalize`, SQL-план с SHA-256 и контракт драйвера. Подготовлено чтение каталогов PostgreSQL 15+ через `postgres.New().Inspect(ctx, conn, schemaName)` на переданном `*sql.Conn`: таблицы, enum, extensions, функции, триггеры и views. Эта реализация пока проверена только unit-тестами; проверка SQL на реальной БД ожидает разрешения на зависимости и контейнер. `postgres.Open`, diff, экспорт и применение миграций ещё не реализованы. CLI выводит сообщение об отсутствии реализации и завершается с кодом 1.

## Структура

| Каталог или файл | Назначение |
| --- | --- |
| `schema.go`, `validate.go` | Общая модель схемы и валидация |
| `plan.go`, `driver.go` | План SQL и контракт диалектов |
| `migrate.go` | Планирование, применение, история и снимки схемы |
| `export.go`, `load.go` | Экспорт и загрузка Go-схемы |
| `cmd/schemigo/` | CLI и генерация шаблонов |
| `postgres/` | Диалект PostgreSQL |
| `mysql/` | Диалект MySQL |
| `clickhouse/` | Диалект ClickHouse и координация блокировок |
| `internal/testdb/` | Будущий запуск одноразовых БД для интеграционных тестов |
| `docs/superpowers/plans/` | План реализации |

Файлы `*_test.go` добавляются рядом с реализацией по мере разработки. Интеграционные тесты будут отделены тегом `integration`. Каталог `schema/` — будущий результат экспорта пользовательской схемы, а не внутренний пакет инструмента.

## Разработка

Версия Go в существующем `go.mod`: **1.27**. Команды ниже требуют соответствующего toolchain:

```sh
go vet -mod=readonly ./...
go test -mod=readonly ./...
go build -mod=readonly ./...
```

Unit-тесты модели, валидации, SQL-плана и PostgreSQL inspector работают без БД и сторонних зависимостей. По ограничениям [плана](docs/superpowers/plans/2026-11-09-schemigo.md) нельзя изменять `go.mod` и применять изменения к БД, в том числе при подготовке интеграционных fixtures.

Под тегом `integration` подготовлены тесты PostgreSQL и helper одноразового контейнера. Они требуют ещё не подключённых модулей `github.com/jackc/pgx/v5` и `github.com/testcontainers/testcontainers-go/modules/postgres`. После разрешения зависимостей и запуска fixtures команда проверки — `SCHEMIGO_TESTCONTAINERS=1 go test -mod=readonly -tags=integration ./postgres`. Без этой переменной helper пропускает запуск контейнера. Пока не установлены модули, сборка с тегом `integration` недоступна.

Inspector исключает `schema_migrations`, объекты расширений и constraint triggers. Для свойств вне текущей модели, например identity/generated columns, partitioning, UNIQUE constraints, INCLUDE indexes, cross-schema FK и расширенных опций функций, возвращается ошибка. Остальные методы PostgreSQL Driver пока возвращают `errors.ErrUnsupported`.

Требования: [SPEC.md](SPEC.md). Стандарт кода: [CODING_STANDARDS.md](CODING_STANDARDS.md). Этапы: [план реализации](docs/superpowers/plans/2026-11-09-schemigo.md).


# pgbackup-zabbix

Go-переписанная версия bash-скрипта резервного копирования PostgreSQL.
Вместо отправки HTML-отчёта на почту утилита отдаёт метрики напрямую
в Zabbix через протокол **Zabbix Sender** (Zabbix Trapper items).

## Что делает

1. Читает настройки из `config.yaml`, лежащего рядом с бинарником.
   Если файла нет — создаёт шаблон и завершается (код выхода `2`).
2. Получает список нешаблонных баз через `psql`, исключает базы из `backup.exclude_databases`.
3. Запускает `pg_dump -Fc` для каждой базы в `backup.directory/<db>_<YYYYMMDD>.dump`.
   Если файл с таким именем уже существует — пропускает (поведение исходного скрипта).
4. Удаляет дампы старше `backup.retention_days` дней для тех же баз.
5. Запускает `vacuumdb -a -z`.
6. Отправляет в Zabbix следующие trapper-метрики:

   | Key                       | Тип    | Значение                                           |
   |---------------------------|--------|----------------------------------------------------|
   | `pg.backup.status`        | int    | `1` — все базы выгружены, `0` — была ошибка        |
   | `pg.backup.duration`      | float  | время выполнения в секундах                        |
   | `pg.backup.size[<DB>]`    | int    | размер дампа в байтах (по одной метрике на базу)   |
   | `pg.vacuum.status`        | int    | `1` — `vacuumdb` завершился успешно, иначе `0`     |

## Конфигурация

Утилита использует один YAML-файл — пример лежит в [`config.yaml.example`](config.yaml.example).
Пароль в конфиге не хранится: `pg_dump`/`vacuumdb`/`psql` подхватывают его из
`~/.pgpass` пользователя, под которым запущена утилита:

```
echo "127.0.0.1:5432:*:postgres:secret" > ~/.pgpass
chmod 600 ~/.pgpass
```

## Сборка

```sh
make linux            # CGO_ENABLED=0 GOOS=linux GOARCH=amd64
```

Готовый бинарник `pgbackup-zabbix` появится в корне репозитория. Он
статически слинкованный, зависит только от внешних `pg_dump`/`vacuumdb`/`psql`.

## Тесты

```sh
make test             # unit-тесты (без Postgres)
make test-integration # интеграция; нужен живой Postgres, см. ниже
```

Интеграционные тесты ожидают переменные окружения, указывающие на
тестовый кластер (поднимается одной командой `docker run`):

```sh
docker run -d --name pgbackup-test \
  -e POSTGRES_PASSWORD=testpass -p 5433:5432 postgres:16
PG_TEST_HOST=127.0.0.1 PG_TEST_PORT=5433 PG_TEST_USER=postgres PG_TEST_PASSWORD=testpass \
  go test -tags=integration ./...
```

## Запуск

```sh
./pgbackup-zabbix                       # обычный запуск
./pgbackup-zabbix -config /etc/pgbackup-zabbix/config.yaml
./pgbackup-zabbix -no-zabbix            # отключить отправку (отладка)
./pgbackup-zabbix -print-metrics        # вывести метрики в stdout
```

Для расписания используйте cron/systemd-таймер от имени пользователя
с `~/.pgpass` (как было в исходном скрипте).

## Структура

```
cmd/pgbackup-zabbix     main + сборка метрик
internal/config         загрузчик YAML, генерация шаблона
internal/zabbix         клиент Zabbix Sender (ZBXD\x01 + JSON)
internal/backup         оркестрация pg_dump / vacuumdb / retention
```

## Лицензия

MIT.

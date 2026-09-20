# Lokális teszt lépésről lépésre (meglévő MySQL: `rendszer-mysql`)

Ez az útmutató a gépen már futó `rendszer-mysql` Docker konténert (MySQL 5.5,
`127.0.0.1:3306`, root/qwe123) használja. Csak két új adatbázist és két új
usert hoz létre benne, a `rendszer` adatbázishoz nem nyúl:

| szerep | adatbázis | user | jelszó | jog |
|---|---|---|---|---|
| LOCAL (working tree) | `sqit_local` | `sqit_local` | `sqit_local_pw` | SELECT/INSERT/UPDATE/DELETE |
| TEST / UPSTREAM | `sqit_upstream` | `sqit_upstream` | `sqit_upstream_pw` | **csak SELECT** |

Minden parancs PowerShellben értendő. A `q` segédfüggvény az adatbázis kézi
módosítására/lekérdezésére való (ezt a `sqit` sosem teszi az upstreamen):

```powershell
function q($sql) { docker exec rendszer-mysql mysql -uroot -pqwe123 --default-character-set=utf8 -e $sql 2>$null }
```

## 0. Egyszeri előkészítés

```powershell
cd D:\irt_programok\GO\sqit
go install ./cmd/sqit                      # -> C:\Users\Bence\go\bin\sqit.exe
$env:Path += ";$env:USERPROFILE\go\bin"    # tartósan: Windows "Környezeti változók" -> Path
.\scripts\setup-existing-mysql.ps1 -Container rendszer-mysql -RootPassword qwe123
```

A script újrafuttatása bármikor visszaállítja a két adatbázist a kezdőállapotra
(`users`: Anna, John, Maria, 42 Peter old@example.com; `orders`: 3 sor).

## 1. Projekt + `sqit init`  (ez a "git init" élmény)

```powershell
mkdir D:/sqit-proba; cd D:/sqit-proba
sqit init
```

Az `init` végigkérdezi a két kapcsolatot (Enter = alapérték szögletes
zárójelben). Válaszok ehhez a teszthez:

```text
LOCAL:    host 127.0.0.1, port 3306, user sqit_local,    database sqit_local,    password sqit_local_pw
UPSTREAM: host 127.0.0.1, port 3306, user sqit_upstream, database sqit_upstream, password sqit_upstream_pw
local-only: Y
```

A végén mindkét kapcsolatot kipróbálja (`LOCAL   : connected ..., 2 table(s)`),
és a jelszavak már a `sqit.env`-ben vannak. Ugyanez kérdések nélkül, flagekkel
(akkor a jelszavakat neked kell a `sqit.env`-be írnod):

```powershell
sqit init `
  --local-user sqit_local    --local-db sqit_local `
  --test-user  sqit_upstream --test-db  sqit_upstream `
  --local-only
```

Várt kimenet:

```text
Initialized empty sqit repository in D:\sqit-proba\.sqit
Created D:\sqit-proba\sqit.env – put the connection passwords (or DSNs) there.
Added sqit.env to .gitignore.
Created D:\sqit-proba\.sqitignore – list tables that must not be versioned there.
Tracking every table with a primary key (minus .sqitignore).
LOCAL:    sqit_local@127.0.0.1:3306/sqit_local
UPSTREAM: sqit_upstream@127.0.0.1:3306/sqit_upstream
Local-only mode: upstream connections are restricted to localhost.
```

Az `init` három fájlt hoz létre: `.sqit/config.json` (host/port/user/db –
ezt commitolod), **`.sqitignore`** (a NEM verziózandó táblák glob mintái, pl.
`sessions`, `tmp_*` – ezt is commitolod; alapból minden PK-val rendelkező
tábla követve van) és **`sqit.env`** (jelszavak – gitignore-ban van; itt
személyes kizárás is adható: `SQIT_IGNORE_TABLES=a,b`). Írd bele a jelszavakat:

```ini
SQIT_LOCAL_PASSWORD=sqit_local_pw
SQIT_TEST_PASSWORD=sqit_upstream_pw
```

Ezt minden `sqit` parancs automatikusan betölti (a shell env változói
felülírják). A `--local-only` miatt nem-localhost upstreamet a `sqit`
csatlakozás előtt elutasít – amíg a valódi TEST DB-re nem váltasz, hagyd bent.
Később csak a `.sqit/config.json` `test` blokkját (vagy a `sqit.env`-ben a
`SQIT_TEST_DSN`-t) cseréled, és a `localOnly`/`SQIT_LOCAL_ONLY` sort törlöd.

### VS Code

A `vscode-sqit` extension telepítve van: nyisd meg a `D:/sqit-proba` mappát
VS Code-ban, a bal oldali *SQit* ikon alatt látod a táblákat a változásokkal;
egy táblára kattintva táblázatos nézet nyílik (törölt sor piros, új sor zöld,
módosult cellában a régi érték pirossal áthúzva, az új zölddel). A nézet
címsorában: Commit, Fetch, Pull (dry run), Pull, Diff, Log. Ha a `sqit` nincs a
PATH-on, a beállításokban `sqit.executable` = `C:/Users/Bence/go/bin/sqit.exe`.

## 2. Kezdőállapot commitolása

```powershell
sqit status          # HEAD: (no commits yet), Added: 7 -> minden sor új
sqit commit -m "Initial state"
```

```text
[main 6cae7ea3970f] Initial state
snapshot b3b5b7d662fb  schema cb8ac638bbe2
```

## 3. Lokális módosítás → status / diff

```powershell
q "UPDATE sqit_local.users SET name='Peter Smith' WHERE id=42"
sqit status
sqit diff
```

```text
modified: users/42
   id: 42
-  name: Peter
+  name: Peter Smith
   email: old@example.com
   active: 1
```

## 4. Upstream módosítás → fetch / diff --upstream

```powershell
q "UPDATE sqit_upstream.users SET email='new@example.com' WHERE id=42"
sqit fetch                     # upstream: (none) -> c8ccfc396965 (new TEST snapshot)
sqit diff --upstream
```

```text
modified: users/42
   id: 42
   name: Peter
-  email: old@example.com
+  email: new@example.com
   active: 1
```

## 5. Nem ütköző merge → pull

```powershell
sqit pull --dry-run
sqit pull
q "SELECT * FROM sqit_local.users WHERE id=42"
```

A pull kimenete (`name` a LOCAL-ból, `email` az UPSTREAM-ből):

```text
Merge: unchanged 3, local-only 0, upstream-only 0, same 0, field-merged 1
Applied LOCAL changes: INSERT 0, UPDATE 1, DELETE 0
UPDATE users/42.email  old@example.com -> new@example.com
Merge commit 0651b89c9ced created.
```

MySQL: `42 | Peter Smith | new@example.com | 1`. A `Peter Smith` továbbra is
nem commitolt (`sqit status` → Modified: 1), ezért:

```powershell
sqit commit -m "Rename Peter"
```

## 6. Konfliktus → pull --dry-run / pull

```powershell
q "UPDATE sqit_local.users    SET name='Peter Smith Jr' WHERE id=42"
q "UPDATE sqit_upstream.users SET name='Peter Nagy'     WHERE id=42"
q "INSERT INTO sqit_upstream.users (id,name) VALUES (5,'Eve')"   # tiszta upstream változás is
sqit pull --dry-run;  "exit=$LASTEXITCODE"
sqit pull;            "exit=$LASTEXITCODE"
```

Mindkettő `exit=1`, és:

```text
CONFLICTS: 1 (working tree vs upstream) – nothing was applied

users/42.name  [modify/modify]
  BASE:     Peter
  LOCAL:    Peter Smith Jr
  UPSTREAM: Peter Nagy
```

Ellenőrzés, hogy semmi nem módosult részlegesen (Eve sem került be, HEAD ugyanaz):

```powershell
q "SELECT id,name FROM sqit_local.users ORDER BY id"   # 1 Anna, 2 John, 3 Maria, 42 Peter Smith Jr
sqit log -n 1
Get-Content .sqit\refs\heads\main
```

Feloldás: állítsd be LOCAL-ban a kívánt értéket, commitolj, pullolj:

```powershell
q "UPDATE sqit_local.users SET name='Peter Nagy' WHERE id=42"
sqit commit -m "Resolve name"
sqit pull                      # INSERT users/5, merge commit
```

## 7. INSERT / DELETE mindkét oldalon

```powershell
q "INSERT INTO sqit_upstream.users (id,name) VALUES (6,'Frank')"   # UPSTREAM INSERT
q "DELETE FROM sqit_upstream.users WHERE id=1"                     # UPSTREAM DELETE, LOCAL érintetlen -> törlődik
q "DELETE FROM sqit_upstream.users WHERE id=2"                     # UPSTREAM DELETE, LOCAL módosítja -> konfliktus
q "UPDATE sqit_local.users SET name='Johnny' WHERE id=2"
q "INSERT INTO sqit_local.users (id,name) VALUES (7,'Grace')"      # LOCAL INSERT
q "DELETE FROM sqit_local.users WHERE id=3"                        # LOCAL DELETE

sqit pull --dry-run        # users/2  [modify/delete] konfliktus, exit=1
q "UPDATE sqit_local.users SET name='John' WHERE id=2"             # lokális módosítás visszavonása
sqit pull                  # INSERT users/6, DELETE users/1, DELETE users/2
q "SELECT id,name FROM sqit_local.users ORDER BY id"               # 5 Eve, 6 Frank, 7 Grace, 42 Peter Nagy
sqit status                # Added: 1 (Grace), Deleted: 1 (Maria) – ezek a lokális, nem commitolt változások
```

## 8. Upstream védelem és local-only őr

```powershell
docker exec rendszer-mysql mysql -usqit_upstream -psqit_upstream_pw sqit_upstream -e "UPDATE users SET name='x' WHERE id=42"
# ERROR 1142 (42000): UPDATE command denied ...

$env:SQIT_TEST_DSN = 'sqit_upstream:pw@tcp(test-db.corp.example:3306)/prod'
sqit fetch
# sqit fetch: test ($SQIT_TEST_DSN): refused: upstream host "test-db.corp.example" is not localhost (local-only mode)
Remove-Item env:SQIT_TEST_DSN
```

## Újrakezdés

```powershell
cd D:\irt_programok\GO\sqit
.\scripts\setup-existing-mysql.ps1 -Container rendszer-mysql -RootPassword qwe123
Remove-Item -Recurse -Force D:\sqit-proba\.sqit
```

## Automatikus tesztek ugyanezen a szerveren

```powershell
cd D:\irt_programok\GO\sqit
$env:SQIT_IT_DSN    = 'root:qwe123@tcp(127.0.0.1:3306)/'
$env:SQIT_LOCAL_DSN = 'sqit_local:sqit_local_pw@tcp(127.0.0.1:3306)/sqit_local'
$env:SQIT_TEST_DSN  = 'sqit_upstream:sqit_upstream_pw@tcp(127.0.0.1:3306)/sqit_upstream'
go test ./...
```

A `TestLocalEnvironmentWorkflow` a fenti 1–8. lépést játssza le és SQL-ből
ellenőrzi az eredményt (a végén a két adatbázist újraseedeli). A JSON-típus
teszt MySQL 5.5-ön automatikusan kimarad.

## 9. Új tábla az upstreamen

```powershell
q "CREATE TABLE sqit_upstream.tags (id INT PRIMARY KEY, name VARCHAR(50)); INSERT INTO sqit_upstream.tags VALUES (1,'red')"
sqit pull --dry-run        # ABORTED: added table: tags + kiírja a CREATE TABLE-t
sqit pull --create-missing-tables
q "SELECT * FROM sqit_local.tags"                                  # 1 red
```

A `sqit` meglévő táblát sosem módosít (`ALTER TABLE` nincs); csak a kizárólag
upstreamen létező táblát hozza létre a TEST definíciójával, és csak ezzel a
kapcsolóval. Ha nem kell a tábla: írd a `.sqitignore`-ba. (A `sqit_local`
usernek ehhez `CREATE` jog kell – a seed scriptben már benne van.)

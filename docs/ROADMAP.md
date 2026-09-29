# ROADMAP — Módulo de Issues · Seam AMS

## 1. Contexto y objetivo

El módulo de Issues implementa el registro, triaje, resolución y cierre
de incidencias sobre los activos, desarrollado por iteraciones en el
shadow repo `seam-issues-tracker`.

Principio acordado por el equipo: primero un flujo funcional básico y
completo en el canal principal de la app; después se abren los otros
canales; al final llega el pulido y las funcionalidades nuevas.

La Iteración 1 se compone de 13 tarjetas, una por persona por semana,
para que el equipo de cuatro pueda tomarlas de a una, implementarlas de
forma individual y avanzar sin pisarse.

## 2. Entorno y arranque

El shadow repo replica la estructura del proyecto Seam bajo
`src/modified/`. Los cambios se sobreescriben sobre un ZIP limpio con
`merge.sh`.

Para materializar y compilar:

```bash
./merge.sh /ruta/al/seam-v0.4.0.zip /tmp/seam-dev
cd /tmp/seam-dev
go build -o /tmp/seam-bin ./cmd/seam
```

xolu debe arrancar con la API v2 habilitada, porque el módulo de issues
usa las rutas `/api/v2/fsm/*`:

```bash
pkill -f /tmp/xolu-bin
XOLU_API_V2_ENABLED=true /tmp/xolu-bin --port 9090 --base-dir /tmp/xolu-data
curl -s http://localhost:9090/api/v2
```

Si el curl responde 404, v2 está apagada y crear incidencias falla con
error 500. Verificación de la suite:

```bash
go test ./internal/ui/ -run TestIssue -v
```

El schema de entidades, la FSM y las traducciones de los issues viven en
el shadow repo y se versionan ahí. El flujo entero se prueba contra
xolu con v2 encendida.

## 3. Convenciones inmutables

Estas reglas no se negocian en ninguna tarjeta:

- Identificadores internos y de dominio siempre en inglés.
- Todo texto visible al usuario va por la función `t()`; ningún string
  hardcodeado en la UI.
- Los 4 archivos de locales se mantienen siempre en sincronía:
  `en.json`, `es.json`, `pt.json`, `ja.json`. `en.json` es la fuente.
- Comentarios en el código en inglés.
- Una tarjeta se considera terminada solo si cumple todo esto:
  compila, `go test ./internal/ui/` pasa, las claves i18n nuevas existen
  en los 4 locales y no hay strings mágicos en la UI.
- Lint y tests deben pasar antes de dar una tarjeta por cerrada.

## 4. Identificadores canónicos

Fuente única de verdad para no inventar nombres en cada tarjeta.

Canal de entrada:

| Concepto | Valores |
|---|---|
| channel | app · qr · api |

Estados de la FSM:

| Concepto | Valor |
|---|---|
| inicial | reported |
| intermedios | triaged · in_progress · on_hold · resolved |
| terminales | closed · rejected · duplicate |

Prioridad y severidad:

| Concepto | Valores |
|---|---|
| priority | critical · high · medium · low |
| severity | critical · major · minor |

Categoría del activo y SLA:

| Concepto | Valor |
|---|---|
| categoría | critico · normal |
| mapeo criticality | High → critico · Medium y Low → normal |
| SLA crítico | critical 4h · high 24h · medium 48h |
| SLA normal | critical 24h · high 48h · medium 7 días |

Campos de la entidad issues: id · title · description · state ·
machine_id · priority · severity · asset_id · asset_category ·
reported_by · channel · assignee · sla_deadline · audit_log ·
created_at.

## 5. Decisiones de arquitectura

Resuelto:

- Una sola machine_def con nombre `seam_issue_lifecycle` se crea la
  primera vez y se reutiliza en todos los issues.
- Cada issue tiene su propia machine en xolu, creada en el alta;
  `machine_id` queda guardado en la entidad.
- `issueFromEntity` se usa para el detalle. El listado lee filas OQL y
  mapea inline, sin reutilizar `issueFromEntity`.
- La categoría del activo se copia al momento del reporte y queda fija
  en `asset_category`.
- La tabla de tiempos de SLA está fija en código durante el MVP;
  editarla por admin es de una iteración posterior.

Pendientes de decisión del equipo:

- Confirmar el mapeo `criticality` del activo a critico/normal.
- Elegir la fuente de auditoría del historial de acciones: campo
  `audit_log` en la entidad o historial de la máquina.

## 6. Iteración 1 — Tarjetas

### Extender la FSM del ciclo de vida a los ocho estados

Ampliar `seam_issue_lifecycle` en `issues.go` con la FSM completa del
módulo: `reported` como estado inicial, `triaged`, `in_progress`,
`on_hold`, `resolved`, con `closed`, `rejected` y `duplicate` como
terminales. Transiciones: `triage`: reported → triaged; `start`:
reported o triaged → in_progress; `hold`: in_progress o triaged →
on_hold; `resume`: on_hold → in_progress; `resolve`: in_progress →
resolved; `close`: resolved → closed; `reject`: desde cualquier estado
no terminal → rejected con el motivo como input; `merge`: reported,
triaged o in_progress → duplicate con el id del issue canónico como
input. Mantener `Determinism: "firstmatch"`. La UI de esta iteración
expone solo triage, start, resolve, close y reject; hold, resume y
merge quedan definidos en la máquina sin UI para no volver a tocar la
definición. Actualizar `TestIssueResolve_InProgressToClosed` a la ruta
resolve → close.

### Resolver y cerrar una incidencia desde el detalle

En el detalle, botón Resolver cuando está `in_progress` y Cerrar cuando
está `resolved`. Al pulsar, disparar la transición correspondiente y
actualizar la vista. Las transiciones inválidas se rechazan con mensaje
claro. i18n de las acciones y de los estados `resolved` y `closed` en
los 4 idiomas. Depende de la tarjeta de FSM.

### Rechazar una incidencia con motivo obligatorio

Acción Rechazar para cualquier estado no terminal. Formulario con
textarea de motivo validado obligatorio en cliente y servidor. La
transición `reject` se dispara con el motivo como input y queda visible
en el detalle. i18n del formulario y del estado `rejected` en los 4
idiomas. Tests de la transición y de motivo vacío. Depende de la
tarjeta de FSM.

### Asignar responsable a una incidencia

Acción de asignar responsable en el detalle con un selector searchable
sobre la entidad `users`, guardado en `assignee`. Al asignar sobre una
incidencia `reported`, disparar `triage` para pasar a `triaged`; desde
`triaged` se habilita pasar a `in_progress`. El creador queda como
responsable por defecto. Mostrar el responsable en listado y detalle.
i18n y tests de asignación y reasignación. Depende de la tarjeta de
FSM.

### Filtrar el listado de incidencias por estado

Pestañas o selector por los 8 estados con conteo por estado y filtro
`WHERE state = ...` en la OQL. Sin filtro se muestran todas por id
desc. i18n de las 8 etiquetas en los 4 idiomas y tests del filtro.
Depende de la tarjeta de FSM para que los 8 estados existan.

### Copiar la categoría del activo al reporte

Al crear con activo vinculado, resolver la categoría a partir de
`criticality` según la tabla canónica y guardarla en `asset_category`.
Sin activo, categoría por defecto normal. Mapeo en un helper puro con
tests. Alimenta la tarjeta de SLA.

### Calcular el SLA al crear la incidencia

Agregar `sla_deadline` y `asset_category` a la entidad y al struct
`Issue`. Calcular el deadline al crear con la tabla fija de tiempos:
críticos 4h, 24h y 48h y normales 24h, 48h y 7 días según la prioridad.
Cálculo como función pura con tabla de casos y tests. La tabla queda
fija en código. Depende de la tarjeta de categoría.

### Mostrar el SLA y su vigencia en listado y detalle

Mostrar `sla_deadline` en formato legible y un badge de vigencia
calculado contra `time.Now()`, en listado y detalle. Sin badge para los
terminales closed, rejected y duplicate. i18n de los estados del SLA en
los 4 idiomas. Depende de la tarjeta de cálculo del SLA.

### Registrar y mostrar el historial de acciones de la incidencia

Cada acción registra quién y cuándo, incluidos el motivo de rechazo y
el id canónico del merge. Fuente: campo `audit_log` en la entidad o
historial de la máquina `/fsm/machine/{id}/history`, eligiendo la que no
duplique información. Sección Historial cronológica en el detalle con
autor y timestamp. i18n en los 4 idiomas. Depende de las tarjetas de
FSM y de acciones.

### Capturar y mostrar el reportante en el reporte web

Al crear desde la app, guardar `reported_by` con los datos del usuario
autenticado tomados de la sesión y mostrar reportado por el usuario en
el detalle. Campo en el struct `Issue` y en el parseo. i18n y tests del
guardado y la lectura. Sin dependencias nuevas.

### Buscar incidencias por texto en el listado

Campo de búsqueda que filtra por el título usando `WHERE title LIKE
'%...%'`, combinable con el filtro por estado. Permitir limpiar la
búsqueda. i18n del placeholder y tests con y sin texto. Coordinar con
el filtro por estado porque comparten la barra de filtros.

### Mostrar la información del activo vinculado en el detalle

En el detalle con activo, resolver y mostrar categoría, tipo y
ubicación desde la entidad `assets` usando el patrón ya empleado para
`AssetName`. Si el activo no existe, mostrar una nota de no disponible
sin romper la página. i18n de las etiquetas y tests con y sin activo.

### Testear el ciclo de vida y el SLA de punta a punta

Cerrar la iteración con una suite que cubra: creación con SLA y
categoría, transiciones válidas e inválidas de la FSM de 8 estados,
rechazo con y sin motivo, filtros por estado, búsqueda y vigencia del
SLA con deadlines fijos. Correr `go test ./internal/ui/` completo y
validar el flujo en la app con `XOLU_API_V2_ENABLED=true`: crear →
triar → start → resolver → cerrar, y crear → rechazar. Depende de todas
las demás tarjetas.

## 7. Cómo trabaja el equipo

Cada integrante toma una tarjeta, la implementa solo en el shadow repo
y la valida contra la definición de terminada de la sección 3. Al
terminar, el cambio queda bajo `src/modified/` del shadow repo. El
responsable de la iteración corre `merge.sh` sobre un ZIP limpio y
ejecuta la suite completa de tests antes de considerarla integrada. Las
tarjetas con dependencias se toman después de las que las desbloquean:
la de FSM y la de categoría son las primeras; las de detalle y acciones
arrancan apenas esté la FSM; SLA, filtros y búsqueda van en paralelo; la
de tests es la última y cierra la iteración.

## 8. Roadmap posterior

La iteración 2 abre el canal QR público: generación de códigos por
activo con la categoría embebida, la landing pública existente en
`/s/{code}` y el formulario anónimo con contacto obligatorio. La
iteración 3 suma el canal API para sistemas externos y agentes de IA.

Después llega el pulido y las funcionalidades nuevas: plantillas
dinámicas versionadas con editor de administración en alta y uso,
motor de reglas IF/THEN sobre el contexto de entrada, completar el
historial con triaged extendido y fusión de duplicados, panel de
control, edición y borrado de configuraciones con validaciones,
contrato operativo de la API para MCP y la integración con Workorders
para lanzar y vincular órdenes de trabajo desde la incidencia.
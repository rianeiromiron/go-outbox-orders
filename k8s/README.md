# k8s — despliegue en Kubernetes (kind)

Manifiestos (kustomize) para desplegar todo el sistema en un clúster local
[kind](https://kind.sigs.k8s.io/). **Estado:** Fase 5 completada y verificada en
un clúster real.

## Qué se despliega (namespace `outbox`)

| Recurso | Réplicas | Notas |
|---|---|---|
| `postgres` (StatefulSet + Service) | 1 | PVC de 1 Gi; sondas `pg_isready` |
| `redis` (StatefulSet + Service) | 1 | PVC de 512 Mi; sondas `redis-cli ping` |
| `migrate` (Job) | 1 | `/app/migrate` de la imagen de orders-api; espera a Postgres con un initContainer |
| `orders-api` (Deployment + Service `:80`) | 2 | readiness `/readyz`, liveness `/healthz` |
| `notifier-worker` (Deployment) | 2 | readiness `/readyz`, liveness `/healthz`; sin Service (nadie lo llama) |
| `postgres-credentials` (Secret) / `outbox-config` (ConfigMap) | — | credenciales **solo de desarrollo** (las mismas de docker-compose) |

Postgres y Redis son de **una sola instancia**: no es una topología de alta
disponibilidad, solo lo necesario para probar los servicios.

## Cómo usarlo

Requisitos: Docker, [kind](https://kind.sigs.k8s.io/), `kubectl` y `make`.
Todo se ejecuta desde la raíz del repositorio.

```bash
make k8s-cluster   # crea el clúster kind "outbox-k8s" (una vez)
make k8s-up        # construye las imágenes, las carga en el nodo y despliega; espera a que todo esté listo
make k8s-status    # pods, servicios, job y PVCs

# Probarlo: la API no se publica en el host; se accede con port-forward.
kubectl --kubeconfig .kube/outbox-k8s.yaml -n outbox port-forward svc/orders-api 8082:80
curl -s -X POST http://localhost:8082/orders -d '{"customer_email":"ana@example.com","currency":"USD","items":[{"sku":"S","quantity":1,"unit_price_cents":100}]}'
kubectl --kubeconfig .kube/outbox-k8s.yaml -n outbox logs -l app.kubernetes.io/name=notifier-worker --prefix | grep notificación

make k8s-down            # borra el namespace (y con él los volúmenes)
make k8s-delete-cluster  # borra el clúster y su red Docker
```

Usa el puerto `8082` para el port-forward: el `8081` es el de Docker Compose.

## Aislamiento respecto de tus otros proyectos

El clúster es **nuevo y aislado**; no reutiliza nada de otros proyectos:

- **Red Docker propia.** Por defecto kind reutiliza la red `kind`, que puede
  estar en uso por otros clústeres. `make k8s-cluster` fija
  `KIND_EXPERIMENTAL_DOCKER_NETWORK=outbox-k8s`, así que el nodo vive solo en una
  red nueva llamada `outbox-k8s`. No es `outbox-net`: esa es la del stack de
  Docker Compose; aquí los pods se comunican entre sí con la red interna de
  Kubernetes y Docker solo ve un contenedor (el nodo).
- **Kubeconfig propio.** `kind create cluster` normalmente modifica
  `~/.kube/config` y cambia el contexto actual. Aquí se usa
  `--kubeconfig .kube/outbox-k8s.yaml` (ignorado por git) y todos los comandos
  `kubectl` de este README lo llevan explícito; tu `~/.kube/config` no se toca.
- Las imágenes se cargan con `kind load docker-image`: **no se publica nada en
  ningún registro**.

## Decisiones de diseño

- **Orden de arranque con initContainers, no con el orden de los manifiestos.**
  `kubectl apply -k` lo aplica todo a la vez; `migrate` espera a Postgres y la
  API y el worker esperan a que exista la tabla `outbox_events` (consulta a
  Postgres). Así el despliegue converge solo, sin scripts de orden.
- **Liveness ≠ readiness.** `/healthz` solo dice que el proceso vive;
  `/readyz` comprueba las dependencias (Postgres; y Redis en el worker). Si
  Redis se cae, los workers pasan a `NotReady` pero **no se reinician**:
  reiniciarlos no arregla Redis y solo generaría reinicios en cadena.
- **UID numérico.** Las imágenes declaran `USER app` (por nombre) y el kubelet no
  puede verificar `runAsNonRoot` con un nombre; los manifiestos fijan
  `runAsUser: 10001`.
- **Endurecimiento.** API, worker y migrate corren como no-root, con
  `readOnlyRootFilesystem`, sin escalada de privilegios, sin capabilities y con
  perfil seccomp `RuntimeDefault`. El namespace exige el perfil de Pod Security
  `baseline` y audita contra `restricted`. Postgres y Redis usan UID no-root
  propio y fsGroup para el volumen.
- **Despliegues sin cortes.** `maxUnavailable: 0`; `terminationGracePeriodSeconds:
  30` para dar margen al apagado ordenado.
- **El Job de migración es inmutable.** Si cambia su spec hay que borrarlo antes
  de aplicar; `make k8s-up` ya lo hace (la migración es idempotente) y
  `ttlSecondsAfterFinished` lo limpia a la hora.

## Verificado en un clúster real

Con 2 réplicas de API y de worker:

| Prueba | Resultado |
|---|---|
| 61 pedidos (60 en paralelo) | 61 notificaciones repartidas entre los dos workers (26 y 35), 0 pendientes, 0 `event_id` repetidos |
| `rollout restart` del worker con pedidos entrando | 141 pedidos, 141 notificaciones, 0 repetidas, 0 pendientes; los pods viejos registraron `apagando` (SIGTERM llegó al apagado ordenado) |
| Redis a 0 réplicas | workers `NotReady` sin reinicios, `/healthz` sigue 200, la API sigue aceptando pedidos; al volver Redis se vacía el backlog |

Además se corrigió durante la verificación un fallo real: el `initContainer` de
`migrate` corre como UID 10001, que no existe en `/etc/passwd`, y `pg_isready`
sin `-U` responde `no attempt` sin siquiera conectar.

## Límites conocidos

- **Sin `NetworkPolicy`:** el CNI por defecto de kind (kindnet) no las aplica de
  forma fiable, así que no se incluyen.
- **Sin Ingress ni exposición al host:** solo `port-forward`.
- **Credenciales de desarrollo en el repositorio** (Secret con `orders/orders`).
  En un entorno real el Secret no se versiona.
- Las sondas del arranque fallan unos segundos (`connection refused`) mientras la
  aplicación abre su puerto: es normal y aparece como `Warning` en los eventos.
- En CI solo se validan los manifiestos de forma estática (kustomize +
  kubeconform); el despliegue en un clúster real se prueba en local.

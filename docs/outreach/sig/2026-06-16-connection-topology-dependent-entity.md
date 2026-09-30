# Digest: dependent (source) entity for an outbound-connection `depends_on` edge

Date: 2026-06-16
Author: SIG liaison (internal brief)
Question owner: dev lane (senhub-agent connection topology, Toise consumer)

Scope: spec-grounded answer only. Sources cited inline. Flags the moving parts.

## Quoi de neuf (delta depuis veille 2026-06-15)

Rien de matériel n'a bougé en 24h:
- `entity-events.md` (merged) inchangé; confirme `service.instance --runs_on--> process` et
  `process --runs_on--> host` comme EXEMPLES, et la regle de placement (porter la relation
  sur l'entite a duree de vie courte / fort churn).
- semconv entity registry: `service`, `service.instance`, `service.namespace` = **Stable**;
  `process`, `host` = Development. "Relationships and signal associations are a work in progress."
- spec #5067 "identity scope": OPEN, derniere activite 2026-06-08, ne discute NI endpoints
  reseau NI relations de dependance.

## Les 4 reponses

### 1. Entite d'un service local en cours d'execution = `service.instance`

semconv registry, namespace `service` (Stable). Attributs identifiants:
- `service.namespace` (Stable, Required) — "A namespace for service.name". service.name unique
  dans le namespace.
- `service.name` (Stable, Required) — "Logical name of the service". MUST etre identique pour
  toutes les instances d'un service scale horizontalement. Defaut SDK: `unknown_service:<exe>`.
- `service.instance.id` (Stable, Required) — "MUST be unique for each instance of the same
  `service.namespace,service.name` pair". Le triplet
  `(service.namespace, service.name, service.instance.id)` doit etre globalement unique.

STABILITE (point critique pour la stabilite des aretes): l'attribut est Stable, mais sa VALEUR
n'est PAS garantie persistante a travers les redemarrages. semconv: le SDK PEUT generer un UUID
v1/v4 aleatoire (-> change a chaque demarrage), OU deriver un UUID v5 stable a partir d'un ID
inherent "si la stabilite est souhaitable" (namespace UUID `4d63009a-8d0f-11ee-aad7-4c796ed8e320`).
=> La persistance est OPTIONNELLE cote producteur. Une arete ancree sur service.instance.id peut
donc churner a chaque restart si le service amont ne stabilise pas son id.

### 2. `depends_on` et qui le porte

- `depends_on`: apparait dans `entity-events.md` (merged) seulement comme STRING d'exemple de
  `relationship.type`. Enumeration OUVERTE; "Standard relationship types SHOULD be defined in
  OpenTelemetry semantic conventions." semconv n'a enregistre AUCUN type de relation
  (registry: relationships = WIP; roadmap semconv #3330 = aucun travail de type de relation).
  => `depends_on` est utilisable mais sans semantique enregistree; c'est une convention de facto,
  pas un terme normatif.
- Qui porte l'arete: la spec est explicite. "The source is the entity emitting the Entity State
  event"; "The target is referenced in the relationship descriptor." Donc le DEPENDENT (source)
  est, par construction, l'entite que le producteur emet et sur laquelle il attache le descriptor.
  Le producteur (host agent) emet son entite locale et y embarque `depends_on -> <endpoint distant>`.
- La spec ne dit RIEN de specifique sur "quel endpoint est la source pour une arete de
  connexion/dependance". Il n'y a pas de terme `connects_to`/`calls`/`communication`. Plus fort:
  la spec dit que les relations sont structurelles, pas operationnelles, et que "deriving topology
  from telemetry signals themselves appears to be out of scope for the current entity model".
  => Modeliser des connexions TCP VIVANTES comme relations d'entite n'est pas dans l'esprit du
  modele; une dependance DURABLE derivee de la socket table (pas le flux live) reste defendable.

### 3. Local side: service.instance vs process vs "listener"

- Il y a bien un `process` entity (semconv, Development). Attributs identifiants:
  `process.pid` + `process.creation.time` (+ build_id.htlhash, runtime.name/version) — tous en
  Release Candidate. (Aligne sur Toise #61 / ADR 0018: pid + creation.time.)
- Mapping process -> service.instance: la spec le donne comme EXEMPLE de chaine
  (`service.instance --runs_on--> process --runs_on--> host`) mais semconv ne DEFINIT pas encore
  cette relation (relationships = WIP). Il n'existe AUCUNE convention normative reliant un PID OS
  a un service.instance.id.
- Pas de notion de "listener entity" distincte. Cote ecoute, l'entite naturelle est le
  service.instance qui ecoute; cote observable distant, c'est l'endpoint reseau
  {server.address, server.port, network.transport} (deja modelise par Toise, point regle).
- Source de dependance naturelle: la spec n'impose rien, mais la chaine d'exemple suggere que la
  granularite "applicative" est service.instance, et que process est l'ancrage OS.

### 4. Recommandation spec-alignee pour le dependent d'une arete outbound `depends_on`

Le probleme dur: depuis la socket table, l'agent a un PID local. Il PEUT fiabiliser
`process.pid` + `process.creation.time` (lecture /proc). Il ne peut PAS, en general, obtenir de
maniere fiable le `service.instance.id` (ni service.name/namespace) du process proprietaire de la
socket — ces valeurs vivent dans le SDK/env du process, pas dans la socket table. C'est
exactement le cas vise par la regle data-model:

  "If an observer cannot reliably obtain one or more identifying attributes, it MUST NOT emit
   telemetry using that entity type. Instead, it SHOULD ... emit a DIFFERENT entity type with
   identifying attributes it CAN populate reliably."

Donc:

- NE PAS emettre un `service.instance` partiel/devine depuis un PID. L'agent ne possede pas
  l'identite spec-requise (triplet namespace/name/instance.id) -> interdit par la regle ci-dessus.
- RECOMMANDATION: ancrer la source de l'arete sur l'entite que l'agent PEUT identifier
  fiablement depuis la socket + /proc, c.-a-d. le `process` (`process.pid`,
  `process.creation.time`), ou a defaut le `host` (`host.id`) si la resolution process est
  indisponible/non fiable.
  - `process` est le choix le plus fidele et le plus lisible (le dependant reel, c'est le process
    qui detient la socket), et il s'aligne sur la chaine d'exemple de la spec
    (process runs_on host; et un consommateur peut, en surcouche, joindre process -> service.instance
    quand un SDK applicatif emet ce maillon).
  - `host` est le repli quand le PID n'est pas resolvable; moins lisible (perte du "qui depend"),
    mais toujours valide.

Jeu d'attributs identifiants a emettre pour le DEPENDENT (source):

  Option A (preferee) — entity.type = `process`
    entity.id = { process.pid: <int>, process.creation.time: <ISO 8601> }
    (descriptifs optionnels: process.executable.name, process.command_line — NON identifiants)

  Option B (repli) — entity.type = `host`
    entity.id = { host.id: <stable host id> }

Et l'arete: source(emise) = ce process/host, descriptor =
  { relationship.type: "depends_on",
    entity.type: <endpoint reseau distant>,   // l'entite observable deja reglee
    entity.id: { server.address, server.port, network.transport } }

Placement (regle de churn): l'arete vit du cote court/volatil. Le process (qui ouvre/ferme des
connexions) churne plus que l'endpoint distant -> porter `depends_on` sur le process est conforme
a la regle "shorter lifespan / higher churn".

Lisibilite LLM/humain: "process X (pid+creation) depends_on endpoint Y:Z" est directement lisible.
La montee vers "service A depends_on service B" est une SURCOUCHE consommateur (joindre
process -> service.instance quand l'instrumentation applicative fournit ce maillon), pas quelque
chose que l'agent host doit deviner — coherent avec le principe fondateur Toise (faits emis,
deduction en surcouche).

## Ce qui reste NON tranche dans le SIG (ne pas hard-coder)

- Aucun `relationship.type` n'est enregistre en semconv. `depends_on` = exemple de chaine, pas
  terme normatif. Risque: renommage / semantique future. (semconv registry; roadmap #3330.)
- La relation process -> service.instance est un EXEMPLE, pas une convention definie. Le mapping
  PID -> service.instance.id n'a aucune base normative.
- service.instance.id n'est PAS garanti stable a travers les redemarrages (UUID aleatoire permis).
  Ancrer des aretes durables dessus = cible mouvante cote producteur amont.
- "identity scope" (#5067, OPEN) pourrait introduire un ID-context (local vs global) touchant la
  facon dont process/host/service.instance s'ancrent les uns aux autres. A surveiller.
- Le modele decourage la topologie derivee de telemetrie (out of scope). Une dependance DURABLE
  issue de la socket table est defendable; un flux/connexion LIVE comme relation ne l'est pas.

## Sources

- entity-events.md (merged):
  https://github.com/open-telemetry/opentelemetry-specification/blob/main/specification/entities/entity-events.md
  (consulte 2026-06-16) — source/target, placement par churn, exemples runs_on, open enum.
- semconv entity registry:
  https://opentelemetry.io/docs/specs/semconv/registry/entities/ (2026-06-16) — service/service.instance Stable, relationships WIP.
- semconv service entity:
  https://opentelemetry.io/docs/specs/semconv/registry/entities/service/ (2026-06-16) — triplet identifiant.
- semconv service attributes registry:
  https://opentelemetry.io/docs/specs/semconv/attributes-registry/service/ (2026-06-16) — service.instance.id non-persistance, UUID v5 namespace.
- semconv process entity:
  https://opentelemetry.io/docs/specs/semconv/registry/entities/process/ (2026-06-16) — pid + creation.time identifiants, RC.
- spec PR #5067 identity scope (OPEN):
  https://github.com/open-telemetry/opentelemetry-specification/pull/5067 (last activity 2026-06-08).
- Toise: #61 (process identity), #65 (relation model), #66 (veille); ADR 0018 (exact identity).

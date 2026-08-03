---
layout: home

hero:
  name: Lagodev
  text: A full-stack web framework for Go
  tagline: Batteries included. ORM, migrations, auth, queue, realtime, admin — one cohesive framework where every subsystem shares the same connection, config, container, and logger.
  actions:
    - theme: brand
      text: Get started
      link: /GETTING_STARTED
    - theme: alt
      text: View on GitHub
      link: https://github.com/devituz/lagodev
    - theme: alt
      text: Compare to Laravel
      link: /COMPARISON

features:
  - title: ORM with relations
    details: Generic Query[T] with HasOne, HasMany, BelongsTo, BelongsToMany, soft deletes, and lifecycle hooks — zero codegen.
    link: /ORM
  - title: Migrations & schema
    details: Transactional migrations with advisory locks across SQLite, MySQL, and PostgreSQL. Up and down, always.
    link: /MIGRATIONS
  - title: HTTP & web layer
    details: Router, middleware, typed requests, validation, and server-rendered views — Laravel-style ergonomics.
    link: /WEB
  - title: Auth & authorization
    details: Guards, sessions, JWT, OAuth (PKCE), and policy-based access control out of the box.
    link: /AUTHENTICATION
  - title: Background work
    details: Queue with dashboard, scheduler, events, and notifications. Long jobs off the request path.
    link: /QUEUE
  - title: Realtime
    details: WebSocket hub with presence and broadcasting, ready for live features.
    link: /REALTIME
  - title: APIs
    details: OpenAPI 3.1 generation with typed client codegen, plus a GraphQL schema & execution layer.
    link: /OPENAPI
  - title: Admin panel
    details: Auto-generated CRUD interface over your models, with Telescope-style debugging.
    link: /ADMIN
---

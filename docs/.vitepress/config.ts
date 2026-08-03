import { defineConfig } from 'vitepress'

// Existing docs use UPPERCASE filenames (ORM.md, GETTING_STARTED.md, ...).
// VitePress serves them at /ORM, /GETTING_STARTED, etc. and rewrites the
// relative .md cross-links automatically.
export default defineConfig({
  title: 'Lagodev',
  description: 'A full-stack web framework for Go — batteries included.',
  lang: 'en-US',
  cleanUrls: true,
  lastUpdated: true,
  base: '/',

  // These links resolve in the GitHub repo tree (source dirs) or at runtime,
  // not on the docs site — don't fail the build on them.
  ignoreDeadLinks: [
    /\.\.\//,
    /^https?:\/\/localhost/,
  ],

  head: [
    ['meta', { name: 'theme-color', content: '#00ADD8' }],
    ['meta', { property: 'og:type', content: 'website' }],
    ['meta', { property: 'og:title', content: 'Lagodev — full-stack Go framework' }],
    ['meta', { property: 'og:description', content: 'ORM, migrations, auth, queue, realtime, admin — one cohesive Go framework.' }],
  ],

  themeConfig: {
    siteTitle: 'Lagodev',

    nav: [
      { text: 'Guide', link: '/GETTING_STARTED' },
      { text: 'ORM', link: '/ORM' },
      { text: 'API Reference', link: 'https://pkg.go.dev/github.com/devituz/lagodev' },
      { text: 'Changelog', link: 'https://github.com/devituz/lagodev/blob/main/CHANGELOG.md' },
    ],

    sidebar: [
      {
        text: 'Getting started',
        collapsed: false,
        items: [
          { text: 'Introduction', link: '/GETTING_STARTED' },
          { text: 'Architecture', link: '/ARCHITECTURE' },
          { text: 'Configuration', link: '/CONFIGURATION' },
          { text: 'CLI (lago / artisan)', link: '/CLI' },
          { text: 'vs Laravel / Django / Nest', link: '/COMPARISON' },
          { text: 'Benchmarks', link: '/BENCHMARKS' },
        ],
      },
      {
        text: 'HTTP & presentation',
        collapsed: false,
        items: [
          { text: 'Web (routing, middleware)', link: '/WEB' },
          { text: 'Views & templating', link: '/VIEWS' },
          { text: 'API resources', link: '/API_RESOURCES' },
          { text: 'OpenAPI 3.1', link: '/OPENAPI' },
          { text: 'GraphQL', link: '/GRAPHQL' },
          { text: 'HTTP client', link: '/HTTP_CLIENT' },
          { text: 'Realtime / WebSocket', link: '/REALTIME' },
        ],
      },
      {
        text: 'Data',
        collapsed: false,
        items: [
          { text: 'ORM', link: '/ORM' },
          { text: 'Database', link: '/DATABASE' },
          { text: 'Migrations', link: '/MIGRATIONS' },
          { text: 'Factories & seeders', link: '/FACTORIES' },
          { text: 'Search', link: '/SEARCH' },
          { text: 'Cache', link: '/CACHE' },
        ],
      },
      {
        text: 'Security & identity',
        collapsed: false,
        items: [
          { text: 'Authentication', link: '/AUTHENTICATION' },
          { text: 'Authorization', link: '/AUTHORIZATION' },
          { text: 'Sessions', link: '/SESSION' },
          { text: 'Encryption', link: '/ENCRYPTION' },
        ],
      },
      {
        text: 'Background work & messaging',
        collapsed: false,
        items: [
          { text: 'Queue', link: '/QUEUE' },
          { text: 'Scheduling', link: '/SCHEDULING' },
          { text: 'Events & notifications', link: '/EVENTS' },
          { text: 'Mail', link: '/MAIL' },
        ],
      },
      {
        text: 'Architecture & ops',
        collapsed: false,
        items: [
          { text: 'Container (DI)', link: '/CONTAINER' },
          { text: 'Validation', link: '/VALIDATION' },
          { text: 'Resilience', link: '/RESILIENCE' },
          { text: 'Observability', link: '/OBSERVABILITY' },
          { text: 'Telescope', link: '/TELESCOPE' },
          { text: 'Admin panel', link: '/ADMIN' },
          { text: 'Localization', link: '/LOCALIZATION' },
          { text: 'Framework integration', link: '/FRAMEWORK_INTEGRATION' },
        ],
      },
    ],

    socialLinks: [
      { icon: 'github', link: 'https://github.com/devituz/lagodev' },
    ],

    search: {
      provider: 'local',
    },

    editLink: {
      pattern: 'https://github.com/devituz/lagodev/edit/main/docs/:path',
      text: 'Edit this page on GitHub',
    },

    footer: {
      message: 'Released under the MIT License.',
      copyright: 'Copyright © 2025-present Lagodev',
    },

    outline: { level: [2, 3] },
  },
})

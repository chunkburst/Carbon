import { defineConfig } from 'vitepress'

// https://vitepress.dev/reference/site-config
export default defineConfig({
  title: 'Carbon',
  description: 'Durable local task coordination for you and your coding agents.',
  lang: 'en-US',
  base: '/Carbon/',

  lastUpdated: true,
  cleanUrls: true,

  head: [['link', { rel: 'icon', type: 'image/svg+xml', href: '/Carbon/logo.svg' }]],

  themeConfig: {
    logo: { light: '/logo.svg', dark: '/logo-dark.svg' },

    nav: [
      {
        text: 'Guide',
        link: '/introduction',
        activeMatch: '^/(introduction|installation|quickstart|guides)',
      },
      { text: 'Architecture', link: '/architecture/carbon-0.4', activeMatch: '^/(architecture|migration)' },
      { text: 'Agents', link: '/agents/', activeMatch: '^/agents/' },
      { text: 'Reference', link: '/reference/mcp-tools', activeMatch: '^/reference/' },
      {
        text: 'v2',
        items: [
          { text: 'Changelog', link: 'https://github.com/chunkburst/Carbon/blob/main/CHANGELOG.md' },
          { text: 'Security policy', link: 'https://github.com/chunkburst/Carbon/blob/main/SECURITY.md' },
          { text: 'Specification', link: 'https://github.com/chunkburst/Carbon/blob/main/SPEC.md' },
        ],
      },
    ],

    sidebar: [
      {
        text: 'Getting started',
        items: [
          { text: 'Introduction', link: '/introduction' },
          { text: 'Installation', link: '/installation' },
          { text: 'Quickstart', link: '/quickstart' },
        ],
      },
      {
        text: 'Carbon architecture',
        items: [
          { text: 'Architecture', link: '/architecture/carbon-0.4' },
          { text: 'Legacy migration reader', link: '/migration/0.4' },
          { text: 'Canonical-name audit', link: '/migration/canonical-name-audit' },
        ],
      },
      {
        text: 'Core concepts',
        items: [
          { text: 'Task files & config', link: '/guides/task-files' },
          { text: 'The agent loop', link: '/guides/agent-loop' },
          { text: 'Sessions', link: '/guides/sessions' },
          { text: 'Checks & gates', link: '/guides/checks-and-gates' },
          { text: 'Work Logs & Worker audit', link: '/guides/work-logs' },
        ],
      },
      {
        text: 'Agents',
        items: [
          { text: 'Overview', link: '/agents/' },
          { text: 'Claude Code', link: '/agents/claude' },
          { text: 'Cursor', link: '/agents/cursor' },
          { text: 'Codex', link: '/agents/codex' },
          { text: 'Windsurf', link: '/agents/windsurf' },
          { text: 'OpenCode', link: '/agents/opencode' },
          { text: 'Kilo Code', link: '/agents/kilo' },
          { text: 'Pi', link: '/agents/pi' },
          { text: 'Antigravity', link: '/agents/antigravity' },
        ],
      },
      {
        text: 'Reference',
        items: [
          { text: 'CLI commands', link: '/reference/cli' },
          { text: 'MCP tools', link: '/reference/mcp-tools' },
          { text: 'HTTP API', link: '/reference/http-api' },
          { text: 'Events (SSE)', link: '/reference/events' },
        ],
      },
    ],

    socialLinks: [{ icon: 'github', link: 'https://github.com/chunkburst/Carbon' }],

    search: { provider: 'local' },

    editLink: {
      pattern: 'https://github.com/chunkburst/Carbon/edit/main/docs/:path',
      text: 'Edit this page on GitHub',
    },

    footer: {
      message: 'Released under the MIT License.',
      copyright: 'Carbon, durable local task coordination',
    },
  },
})

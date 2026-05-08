import { defineManifest } from '@crxjs/vite-plugin'
import pkg from './package.json'

export default defineManifest({
  manifest_version: 3,
  name: 'DualSub Next',
  version: pkg.version,
  description: 'Bilingual subtitles for Netflix and Udemy, powered by a local Go daemon.',
  action: {
    default_popup: 'src/popup/index.html',
    default_title: 'DualSub Next',
  },
  options_page: 'src/options/index.html',
  background: {
    service_worker: 'src/background/index.ts',
    type: 'module',
  },
  content_scripts: [
    {
      matches: [
        'https://www.netflix.com/*',
        'https://www.udemy.com/*',
      ],
      js: ['src/content/index.ts'],
      run_at: 'document_idle',
    },
  ],
  permissions: ['storage', 'activeTab', 'clipboardWrite'],
  host_permissions: [
    'https://www.netflix.com/*',
    'https://www.udemy.com/*',
    'http://127.0.0.1:7878/*',
  ],
})

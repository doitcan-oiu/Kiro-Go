import { createApp } from 'vue'
import { createPinia } from 'pinia'
import router from '@/router'
import { applyDocumentLang } from '@/lib/i18n'
import { initTheme } from '@/lib/theme'
import App from '@/App.vue'
import '@/styles/index.css'

initTheme()
applyDocumentLang()

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.mount('#app')

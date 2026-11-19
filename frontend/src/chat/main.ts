import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import { bootstrapApp } from '@/shared/bootstrap'
import { initFontScale } from '@/shared/composables'
import '@/assets/styles/main.css'

initFontScale()

bootstrapApp(createApp(App), router, { loginRouteName: 'login', titlePrefix: '患者端' }).mount('#chat-app')

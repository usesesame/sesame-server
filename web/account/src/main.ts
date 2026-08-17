import { mount } from 'svelte'
import App from './App.svelte'
import '../design/tokens.css'
import './app.css'

mount(App, { target: document.getElementById('app')!, props: { initialPath: window.location.pathname } })

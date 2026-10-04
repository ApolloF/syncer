import './style.css'
import { mount } from 'svelte'
import App from './App.svelte'

// `npm run dev:mock` swaps the Go side for made-up data before the app starts;
// other builds drop the mock entirely.
const ready = import.meta.env.DEV && import.meta.env.VITE_MOCK === '1' ? import('./mock/install').then(m => m.installMock()) : Promise.resolve()

export default ready.then(() => mount(App, { target: document.getElementById('app')! }))

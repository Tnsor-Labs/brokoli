import { mount } from 'svelte'
import './styles/global.css'
import App from './App.svelte'

const app = mount(App, {
  target: document.getElementById('app')!,
})

// Remove splash loader once app is mounted and CSS is loaded
requestAnimationFrame(() => {
  const loader = document.getElementById('app-loader');
  if (loader) loader.remove();
});

export default app

import { defineConfig } from 'vite';
export default defineConfig({ base: '/admin/', server: { proxy: { '/admin/api': { target: 'https://localhost:8443', secure: false } } } });

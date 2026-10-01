import { defineConfig } from 'vite';
import tailwindcss from '@tailwindcss/vite';
export default defineConfig({ plugins: [tailwindcss()], base: '/admin/', server: { proxy: { '/admin/api': { target: 'https://localhost:8443', secure: false } } } });

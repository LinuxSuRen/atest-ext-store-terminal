import {defineConfig, loadEnv} from 'vite'
import vue from '@vitejs/plugin-vue'

declare global {
  interface Window {
    ATestPlugin?: VuePlugin;
  }
}

interface VuePlugin {
  mount(el?: string, props?: Record<string, any>): void;
  unmount(): void;
}

export default defineConfig(({mode}) => {
  const env = loadEnv(mode, './');
  return {
    plugins: [vue()],
    define: {
      'process.env.NODE_ENV': JSON.stringify('production'),
      'process.env': JSON.stringify({}),
      'wsUrl': JSON.stringify(env.VITE_WS_URL) || `${window.location.hostname}:${window.location.port}`,
      'global': 'window'
    },
    resolve: {
      alias: {
        'process': 'process/browser'
      }
    },
    build: {
      lib: {
        entry: ('src/main.ts'),
      name: 'ATestPlugin',
      fileName: (format) => `atest-ext-store-terminal.${format}.js`
    },
    rollupOptions: {
      // external: ['vue'],
      output: {
        globals: {
          vue: 'Vue'
        }
      }
    }
  },
  server: {
    proxy: {
      '/extensionProxy/terminal': {
        target: env.VITE_API_URL,
        changeOrigin: true
      },
    },
  },
}});
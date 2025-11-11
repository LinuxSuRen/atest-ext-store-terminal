<script setup lang="ts">
import { ref, onMounted, onUnmounted, nextTick } from 'vue'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import type { TabsPaneContext } from 'element-plus'

interface TerminalInstance {
  id: string
  name: string
  terminal: Terminal
}

const terminals = ref<TerminalInstance[]>([])
const activeTerminal = ref('')

interface CommandOutput {
  stdout: string
  stderr: string
}
let terminalCounter = 1

const addTerminal = () => {
  const id = `terminal-${terminalCounter++}`
  const name = `Terminal ${terminalCounter - 1}`
  
  const newTerminal = new Terminal({
    cursorBlink: true,
    theme: {
      background: '#000000',
      foreground: '#ffffff'
    }
  })

  let commandBuffer = ''
  newTerminal.onData(data => {
    if (data.charCodeAt(0) === 13) { // Enter key
      newTerminal.write('\r\n')
      fetch('/api/exec', {
        method: 'POST',
        body: JSON.stringify({
          id,
          cmd: commandBuffer.trim()
        })
      }).then(response => response.json())
        .then((output: CommandOutput) => {
          newTerminal.write(output?.stdout.replace(/\n/g, '\r\n') || '')
          newTerminal.write('$ ')
          commandBuffer = ''
        })
    } else if (data === '\x08' || data === '\x7F') { // delete key
      if (commandBuffer.length > 0) {
        commandBuffer = commandBuffer.slice(0, -1)
        newTerminal.write('\b \b')
      }
    } else {
      newTerminal.write(data)
      commandBuffer += data
    }
  })
  
  terminals.value.push({
    id,
    name,
    terminal: newTerminal
  })
  
  activeTerminal.value = id
  
  // Wait for the next tick to ensure the DOM is updated
  nextTick(() => {
    const container = document.getElementById(id)
    if (container) {
      newTerminal.open(container)
      newTerminal.writeln(`Welcome to ${name}!`)
      newTerminal.writeln('')
      newTerminal.write('$ ')
    }
  })
}

const removeTerminal = (id: string) => {
  const index = terminals.value.findIndex((term: TerminalInstance) => term.id === id)
  if (index !== -1) {
    // Dispose of the terminal
    terminals.value[index].terminal.dispose()
    
    // Remove from the list
    terminals.value.splice(index, 1)
    
    // If we removed the active terminal, select another one
    if (activeTerminal.value === id) {
      if (terminals.value.length > 0) {
        activeTerminal.value = terminals.value[0].id
      } else {
        activeTerminal.value = ''
      }
    }
  }
}

const handleTabClick = (tab: TabsPaneContext) => {
  activeTerminal.value = tab.paneName as string
}

onMounted(() => {
  // Add the first terminal
  addTerminal()
})

onUnmounted(() => {
  // Dispose of all terminals
  terminals.value.forEach((term: TerminalInstance) => {
    term.terminal.dispose()
  })
})
</script>

<template>
  <div class="terminal-container">
    <el-button type="primary" @click="addTerminal" size="small">New Terminal</el-button>
    
    <el-tabs 
      v-model="activeTerminal" 
      type="card" 
      closable 
      @tab-click="handleTabClick"
      @tab-remove="removeTerminal"
      class="terminal-tabs"
    >
      <el-tab-pane
        v-for="term in terminals"
        :key="term.id"
        :label="term.name"
        :name="term.id"
      >
        <div class="terminal-wrapper">
          <div :id="term.id" class="xterm-container"></div>
        </div>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<style scoped>
.terminal-container {
  width: 100%;
  height: 100vh;
  display: flex;
  flex-direction: column;
  padding: 10px;
  box-sizing: border-box;
}

.terminal-tabs {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  margin-top: 10px;
}

.terminal-tabs :deep(.el-tabs__content) {
  flex: 1;
  overflow: hidden;
}

.terminal-wrapper {
  width: 100%;
  height: 100%;
  position: relative;
}

.xterm-container {
  width: 100%;
  height: 100%;
  padding: 10px;
  box-sizing: border-box;
}

:deep(.xterm) {
  width: 100%;
  height: 100%;
}

:deep(.xterm .xterm-viewport) {
  overflow-y: auto;
}
</style>
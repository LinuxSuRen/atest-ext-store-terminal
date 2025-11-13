<script setup lang="ts">
import { ref, onMounted, onUnmounted, nextTick } from 'vue'
import { Terminal } from '@xterm/xterm'
import { ClipboardAddon } from '@xterm/addon-clipboard';
import { WebLinksAddon } from '@xterm/addon-web-links';
import { SearchAddon } from '@xterm/addon-search';
import '@xterm/xterm/css/xterm.css'
import type { TabsPaneContext, TabPaneName } from 'element-plus'

interface TerminalInstance {
  id: TabPaneName
  name: string
  terminal: Terminal
  isExecuting: boolean
  currentPid: number | null
  commandBuffer: string
}

const terminals = ref<TerminalInstance[]>([])
const activeTerminal = ref<TabPaneName>('')
const lastInput = ref('')
let terminalCounter = 1

const operateTerminal = (terminal: TabPaneName, action: 'remove' | 'add', terminalName?: string | null) => {
  if (action === 'remove') {
    return
  }

  const id = terminal || `terminal-${terminalCounter++}`
  const name = terminalName || `Terminal ${terminalCounter - 1}`

  const newTerminal = new Terminal({
    cursorBlink: true,
    scrollback: 1000,
    theme: {
      background: '#000000',
      foreground: '#ffffff'
    }
  })
  newTerminal.loadAddon(new ClipboardAddon())
  newTerminal.loadAddon(new WebLinksAddon());
  newTerminal.loadAddon(new SearchAddon());

  let commandBuffer = ''
  newTerminal.onData(async (data) => {
    const terminalInstance = terminals.value.find(t => t.id === id)
    if (!terminalInstance) return

    lastInput.value = data.charCodeAt(0) + ''
    if (data.charCodeAt(0) === 13) { // Enter key
      newTerminal.write('\r\n')
      if (terminalInstance.isExecuting && terminalInstance.currentPid) {
        // If a command is executing, send input to the running process
        // await sendInputToProcess(terminalInstance.currentPid, commandBuffer + '\n')
        executeCommand(id, commandBuffer)
      } else if (commandBuffer.trim()) {
        // Otherwise, execute a new command
        executeCommand(id, commandBuffer)
      } else {
        newTerminal.write('$ ')
      }
      commandBuffer = ''
    } else if (data === '\x08' || data === '\x7F') { // delete key
      if (commandBuffer.length > 0) {
        commandBuffer = commandBuffer.slice(0, -1)
        newTerminal.write('\b \b')
      }
    } else if (data === '\x03') {
      commandBuffer = ''
      newTerminal.write('\r\n')
      newTerminal.write('$ ')
    } else if (data.charCodeAt(0) === 12) {
      newTerminal.write('\x1b[2J\x1b[H');
      newTerminal.write('$ ')
    } else {
      newTerminal.write(data)
      commandBuffer += data

      // If a command is executing, send input character to the running process
      if (terminalInstance.isExecuting && terminalInstance.currentPid) {
        await sendInputToProcess(terminalInstance.currentPid, data)
      }
    }

    // Update the command buffer in the terminal instance
    terminalInstance.commandBuffer = commandBuffer
  })

  terminals.value.push({
    id,
    name,
    terminal: newTerminal,
    isExecuting: false,
    currentPid: null,
    commandBuffer: ''
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

      newTerminal.attachCustomKeyEventHandler((e) => {
        if (e.type === 'keydown' && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
          e.preventDefault();
          return false;
        }
        return true;
      });
    }
  })
}

function calcCols(term: Terminal, ele: HTMLElement) {
  const core = term._core;
  const charWidth = core._renderService.dimensions.device.char.width;
  const padding = 20;
  const availableWidth = ele.clientWidth - padding;
  return Math.max(1, Math.trunc(availableWidth / charWidth));
}

const calcRows = (term: Terminal, ele: HTMLElement) => {
  const core = term._core;
  const charHeight = core._renderService.dimensions.device.char.height;
  const padding = 20;
  const availableHeight = ele?.parentElement.clientHeight - padding;
  return Math.max(1, Math.trunc(availableHeight / charHeight));
 }

function reflow() {
  terminals.value.forEach(terminalInstance => {
    const terminal = terminalInstance.terminal
    const cols = calcCols(terminal, document.getElementById(terminalInstance.id)!);
    const rows = calcRows(terminal, document.getElementById(terminalInstance.id)!);
    if (Number.isNaN(cols) || Number.isNaN(rows)) return
    terminal.resize(cols, rows)
  })
}

// 初始化 & 监听
reflow();
window.addEventListener('resize', reflow);

const sendInputToProcess = async (pid: number, input: string) => {
  try {
    const response = await fetch('/api/exec/input', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ pid, input })
    });

    if (!response.ok) {
      throw new Error(`HTTP error! status: ${response.status}`);
    }
  } catch (error) {
    console.error('Error sending input to process:', error);
  }
}

const executeCommand = async (terminalId: TabPaneName, cmd: string) => {
  if (cmd === '') return

  const terminalInstance = terminals.value.find(t => t.id === terminalId)
  if (!terminalInstance) return

  terminalInstance.isExecuting = true
  terminalInstance.currentPid = null
  const terminal = terminalInstance.terminal

  try {
    // Using fetch-based approach with streaming
    const response = await fetch('/extensionProxy/terminal', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        cmd: cmd,
        terminalId: terminalId,
        terminalName: terminalInstance.name
      })
    });

    if (!response.ok) {
      throw new Error(`HTTP error! status: ${response.status}`);
    }

    if (!response.body) {
      throw new Error('ReadableStream not supported');
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();

    let buffer = '';
    let processFinished = false;

    while (!processFinished) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop() || ''; // Keep the last incomplete line in the buffer

      for (const line of lines) {
        if (line.startsWith('data: ')) {
          try {
            const data = JSON.parse(line.substring(6));
            switch (data.type) {
              case 'start':
                terminalInstance.currentPid = data.pid;
                break;
              case 'stdout':
                terminal.write(data.data + '\r\n');
                break;
              case 'stderr':
                terminal.write(data.data + '\r\n');
                break;
              case 'end':
                terminal.write('$ ');
                terminalInstance.isExecuting = false;
                terminalInstance.currentPid = null;
                processFinished = true;
                break;
              case 'error':
                terminal.writeln(`[Error: ${data.data}]`);
                terminal.write('$ ');
                terminalInstance.isExecuting = false;
                terminalInstance.currentPid = null;
                processFinished = true;
                break;
            }
          } catch (e) {
            console.error('Error parsing SSE data:', e);
          }
        }
      }
    }

    // Process any remaining data in the buffer
    if (buffer.startsWith('data: ') && !processFinished) {
      try {
        const data = JSON.parse(buffer.substring(6));
        if (data.type === 'end') {
          terminal.writeln(`[Process exited with code ${data.exitCode}]`);
          terminal.write('$ ');
          terminalInstance.isExecuting = false;
          terminalInstance.currentPid = null;
        }
      } catch (e) {
        console.error('Error parsing SSE data:', e);
      }
    }

    // Ensure we always show the prompt when process finishes
    if (processFinished) {
      reader.cancel();
    }
  } catch (error) {
    terminal.writeln(`[Error: ${error}]`);
    terminal.write('$ ');
    terminalInstance.isExecuting = false;
    terminalInstance.currentPid = null;
  }
}

const removeTerminal = (id: string) => {
  fetch('/extensionProxy/terminal', {
    method: 'DELETE',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      terminalId: id,
    })
  })

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

interface TerminalRef {
  terminalId: string
  terminalName: string
}

onMounted(async () => {
  let existingTerminals = await fetch('/extensionProxy/terminal', {
    method: 'GET'
  }).then(response => {
    if (!response.ok) {
      throw new Error(`HTTP error! status: ${response.status}`);
    }
    return response.json();
  })

  if (!existingTerminals || existingTerminals.length === 0) existingTerminals =  [{
    terminalId: 'default',
    terminalName: 'Default'
  }]

  existingTerminals.forEach((key: TerminalRef) => {
    const id = key.terminalId
    const name = key.terminalName
    operateTerminal(id, 'add', name)
  })
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
    <el-tabs
      v-model="activeTerminal"
      type="card"
      editable
      @edit="operateTerminal"
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
    <div id="statusPanel" class="status-panel">{{lastInput}}</div>
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
  margin-top: 10px;
}

.terminal-tabs :deep(.el-tabs__content) {
  flex: 1;
}

.terminal-wrapper {
  width: 100%;
  height: calc(100vh - 150px);
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
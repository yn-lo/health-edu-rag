/**
 * 患者端字号档位（标准 / 大）
 * 通过根节点 class 覆写 tokens.css 的字号令牌实现整体缩放；
 * 切换时同步 DOM 与 localStorage，入口挂载前调用 initFontScale 避免首屏闪烁。
 */
import { ref } from 'vue'

type FontScale = 'standard' | 'large'

const FONT_SCALE_STANDARD: FontScale = 'standard'
const FONT_SCALE_LARGE: FontScale = 'large'

/** localStorage 存储 key */
const FONT_SCALE_KEY = 'hn_font_scale'
/** 大字号根节点类名（tokens.css 中定义变量覆写） */
const FONT_SCALE_LARGE_CLASS = 'font-scale-large'

const scale = ref<FontScale>(FONT_SCALE_STANDARD)

function applyFontScale(value: FontScale): void {
  document.documentElement.classList.toggle(FONT_SCALE_LARGE_CLASS, value === FONT_SCALE_LARGE)
}

function readStoredFontScale(): FontScale {
  return localStorage.getItem(FONT_SCALE_KEY) === FONT_SCALE_LARGE ? FONT_SCALE_LARGE : FONT_SCALE_STANDARD
}

/** 入口挂载前调用：应用已保存的档位，避免首屏闪烁 */
export function initFontScale(): void {
  scale.value = readStoredFontScale()
  applyFontScale(scale.value)
}

/** 字号档位切换（同步 DOM + localStorage） */
export function useFontScale() {
  function setFontScale(value: FontScale): void {
    scale.value = value
    localStorage.setItem(FONT_SCALE_KEY, value)
    applyFontScale(value)
  }

  return { scale, setFontScale }
}

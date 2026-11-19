/**
 * 患者端字号档位 composable 测试
 * 覆盖：默认档位、切换持久化、根节点类名切换、入口初始化读取存储
 */
import { describe, it, expect, beforeEach } from 'vitest'
import { useFontScale, initFontScale } from '@/shared/composables/useFontScale'

const FONT_SCALE_KEY = 'hn_font_scale'
const LARGE_CLASS = 'font-scale-large'

describe('useFontScale', () => {
  beforeEach(() => {
    localStorage.clear()
    document.documentElement.classList.remove(LARGE_CLASS)
    initFontScale()
  })

  it('默认档位为 standard，根节点无大字号类', () => {
    const { scale } = useFontScale()
    expect(scale.value).toBe('standard')
    expect(document.documentElement.classList.contains(LARGE_CLASS)).toBe(false)
  })

  it('切换为大字号同步 DOM 与 localStorage', () => {
    const { scale, setFontScale } = useFontScale()
    setFontScale('large')
    expect(scale.value).toBe('large')
    expect(localStorage.getItem(FONT_SCALE_KEY)).toBe('large')
    expect(document.documentElement.classList.contains(LARGE_CLASS)).toBe(true)
  })

  it('切回标准档移除根节点类并更新存储', () => {
    const { scale, setFontScale } = useFontScale()
    setFontScale('large')
    setFontScale('standard')
    expect(scale.value).toBe('standard')
    expect(localStorage.getItem(FONT_SCALE_KEY)).toBe('standard')
    expect(document.documentElement.classList.contains(LARGE_CLASS)).toBe(false)
  })

  it('入口初始化读取已保存的大字号档位', () => {
    localStorage.setItem(FONT_SCALE_KEY, 'large')
    initFontScale()
    const { scale } = useFontScale()
    expect(scale.value).toBe('large')
    expect(document.documentElement.classList.contains(LARGE_CLASS)).toBe(true)
  })
})

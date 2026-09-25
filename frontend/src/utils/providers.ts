/**
 * 管理台各视图共用的渠道下拉选项，顺序与后端 model.DefaultProviders 保持一致。
 * 新增渠道时同步追加到末尾：TokensView / SettingsView / PlaygroundView / LogsView / DashboardView
 * 都直接消费这份列表，无需逐个视图改动。
 */
export const providerOptions = [
  { label: 'Exa', value: 'exa' },
  { label: 'You.com', value: 'you' },
  { label: 'Jina', value: 'jina' },
  { label: 'Tavily', value: 'tavily' },
  { label: 'Firecrawl', value: 'firecrawl' },
  { label: 'Serper', value: 'serper' },
  { label: 'Brave Search', value: 'brave' },
  { label: 'Keenable', value: 'keenable' }
]

export const defaultProviders = providerOptions.map((item) => item.value)

export function providerLabel(provider: string) {
  return providerOptions.find((item) => item.value === provider)?.label || provider
}

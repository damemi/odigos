import { useEffect } from 'react'
import { useProxy } from '../proxy'
import { GET_METRICS } from '@/graphql'
import { useSourceCRUD } from '../sources'
import type { Metrics } from '@odigos/ui-kit/types'
import { useDestinationCRUD } from '../destinations'

export const useMetrics = () => {
  const { sources } = useSourceCRUD()
  const { destinations } = useDestinationCRUD()

  const { data, fetch } = useProxy<{ getOverviewMetrics?: Metrics }>(GET_METRICS)

  useEffect(() => {
    if (sources.length > 0 || destinations.length > 0) {
      fetch()

      const interval = setInterval(fetch, 3000)
      return () => clearInterval(interval)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sources, destinations])

  return {
    metrics: data?.getOverviewMetrics || {
      sources: [],
      destinations: [],
    },
  }
}

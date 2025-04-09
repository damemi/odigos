import { useEffect } from 'react'
import { useProxy } from '../proxy'
import { GET_DESTINATION_CATEGORIES } from '@/graphql'
import type { FetchedDestinationCategories } from '@/types'

export const useDestinationCategories = () => {
  const { fetch, data } = useProxy<FetchedDestinationCategories>(GET_DESTINATION_CATEGORIES)

  useEffect(() => {
    if (!data?.destinationCategories?.categories) fetch()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return {
    categories: data?.destinationCategories?.categories || [],
  }
}

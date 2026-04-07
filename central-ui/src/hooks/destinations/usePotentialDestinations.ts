import { useCallback, useEffect, useMemo } from 'react';
import { useRemoteFetch } from '../proxy';
import { useOdigos } from '@odigos/ui-kit/contexts';
import { GET_POTENTIAL_DESTINATIONS } from '@/graphql';
import { deepClone, safeJsonParse } from '@odigos/ui-kit/functions';
import type { GetPotentialDestinationsResult } from '@odigos/ui-kit/types';
import { useDestinationCategoriesLegacy } from './useDestinationCategoriesLegacy';
import { type ISetupState, useSetupStore } from '@odigos/ui-kit/store';

interface PotentialDestination {
  type: string;
  fields: string;
}

interface GetPotentialDestinationsData {
  potentialDestinations: PotentialDestination[];
}

const checkIfConfigured = (
  configuredDest: ISetupState['configuredDestinations'][0],
  potentialDest: PotentialDestination,
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  autoFilledFields: Record<string, any>,
) => {
  const typesMatch = configuredDest.type === potentialDest.type;
  if (!typesMatch) return false;

  let fieldsMatch = false;

  for (const { key, value } of configuredDest.form.fields) {
    if (Object.hasOwn(autoFilledFields, key)) {
      // An exact match is when all "ifs" result in true.
      // If one resulted with false, it is not an exact match and therefore not an "already configured destination".
      if (autoFilledFields[key] === value) {
        fieldsMatch = true;
      } else {
        fieldsMatch = false;
        break;
      }
    }
  }

  return fieldsMatch;
};

export const usePotentialDestinations = () => {
  const { isVm } = useOdigos();
  const { categories } = useDestinationCategoriesLegacy();
  const { configuredDestinations } = useSetupStore();

  const { fetch, loading, data } = useRemoteFetch<GetPotentialDestinationsData>(GET_POTENTIAL_DESTINATIONS);

  const getPotentialDestinations = useCallback(async (): Promise<GetPotentialDestinationsResult | undefined> => {
    const { data: res } = await fetch();
    return res as GetPotentialDestinationsResult | undefined;
  }, [fetch]);

  const potentialDestinations = useMemo(() => {
    if (!categories || !data?.potentialDestinations) return [];

    // Create a deep copy of destination types to manipulate
    const parsed: typeof categories = deepClone(categories);

    // Map over the potential destinations
    return data.potentialDestinations
      .map((pd) => {
        for (const category of parsed) {
          const autoFilledFields = safeJsonParse<{ [key: string]: string }>(pd.fields, {});
          const alreadyConfigured = configuredDestinations.find((cd) => checkIfConfigured(cd, pd, autoFilledFields));

          if (!alreadyConfigured) {
            const idx = category.items.findIndex((item) => item.type === pd.type);

            if (idx !== -1) {
              return {
                // Spread the matched destination type data into the potential destination
                ...category.items[idx],
                fields: category.items[idx].fields.map((field) => ({
                  ...field,
                  initialValue: autoFilledFields[field.name],
                })),
              };
            }
          }
        }

        return null;
      })
      .filter((pd) => pd);
  }, [configuredDestinations, categories, data?.potentialDestinations]);

  useEffect(() => {
    if (!isVm && !data?.potentialDestinations) fetch();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return {
    loading,
    potentialDestinations,
    getPotentialDestinations,
  };
};
